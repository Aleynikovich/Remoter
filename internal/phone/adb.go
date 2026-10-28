package phone

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// TCPPort is where the phone's ADB listens after `adb tcpip`, until it
// restarts. Wireless debugging's own port changes every time it is turned
// on, so the viewer moves the phone to this one once paired.
const TCPPort = "5555"

// ErrNoADB means the adb command is not installed.
var ErrNoADB = errors.New(adbMissing())

func adbMissing() string {
	if runtime.GOOS == "windows" {
		return "ADB is not installed: run `winget install Google.PlatformTools` and start the viewer again"
	}
	return "ADB is not installed (Debian/Ubuntu: sudo apt install adb, Arch: pacman -S android-tools, Fedora: dnf install android-tools)"
}

// ErrUnauthorized means the phone has not allowed this computer yet.
var ErrUnauthorized = errors.New("allow USB debugging from this computer on the phone (tick \"Always allow\")")

// ADB runs the adb command.
type ADB struct {
	Path string
}

// FindADB looks for adb on PATH and where Android's tools usually go.
func FindADB() (*ADB, error) {
	if p, err := exec.LookPath("adb"); err == nil {
		return &ADB{Path: p}, nil
	}
	var dirs []string
	if runtime.GOOS == "windows" {
		local := os.Getenv("LOCALAPPDATA")
		dirs = append(dirs,
			filepath.Join(local, "Android", "Sdk", "platform-tools"),
			filepath.Join(local, "Microsoft", "WinGet", "Links"),
		)
		// winget unpacks the zip under Packages\Google.PlatformTools_*.
		if m, _ := filepath.Glob(filepath.Join(local, "Microsoft", "WinGet", "Packages", "Google.PlatformTools*", "platform-tools")); m != nil {
			dirs = append(dirs, m...)
		}
	} else if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, "Android", "Sdk", "platform-tools"))
	}
	name := "adb"
	if runtime.GOOS == "windows" {
		name = "adb.exe"
	}
	for _, d := range dirs {
		p := filepath.Join(d, name)
		if _, err := os.Stat(p); err == nil {
			return &ADB{Path: p}, nil
		}
	}
	return nil, ErrNoADB
}

// command prepares an adb invocation that does not open a console window.
func (a *ADB) command(ctx context.Context, args ...string) *exec.Cmd {
	c := exec.CommandContext(ctx, a.Path, args...)
	hideWindow(c)
	return c
}

// run runs adb and returns what it printed.
func (a *ADB) run(ctx context.Context, args ...string) (string, error) {
	var out bytes.Buffer
	c := a.command(ctx, args...)
	c.Stdout, c.Stderr = &out, &out
	err := c.Run()
	text := strings.TrimSpace(out.String())
	if err != nil {
		if ctx.Err() != nil {
			return text, fmt.Errorf("adb %s: timed out", args[0])
		}
		if text != "" {
			return text, fmt.Errorf("adb %s: %s", args[0], text)
		}
		return text, fmt.Errorf("adb %s: %w", args[0], err)
	}
	return text, nil
}

// Pair pairs with the phone's "Pair device with pairing code" dialog.
func (a *ADB) Pair(ctx context.Context, addr, code string) error {
	out, err := a.run(ctx, "pair", addr, code)
	if err != nil {
		return err
	}
	if !strings.Contains(out, "Successfully paired") {
		return fmt.Errorf("pairing failed: %s", out)
	}
	return nil
}

// Connect connects to the phone's ADB at addr and checks the phone lets
// this computer in.
func (a *ADB) Connect(ctx context.Context, addr string) error {
	// A connection left over from before the phone went away shows as
	// offline and blocks a fresh one.
	if state, _ := a.State(ctx, addr); state == "offline" {
		a.run(ctx, "disconnect", addr)
	}
	out, err := a.run(ctx, "connect", addr)
	if err != nil {
		return err
	}
	// adb connect exits 0 on most failures; the text tells.
	if !strings.Contains(out, "connected to") || strings.Contains(out, "failed") || strings.Contains(out, "cannot") {
		return fmt.Errorf("cannot reach ADB at %s: %s", addr, out)
	}
	// The phone may still be asking whether to allow this computer.
	for range 20 {
		state, err := a.State(ctx, addr)
		switch {
		case state == "device":
			return nil
		case state == "unauthorized":
			err = ErrUnauthorized
		case err == nil:
			err = fmt.Errorf("phone is %s", state)
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(500 * time.Millisecond):
		}
	}
	state, err := a.State(ctx, addr)
	if state == "unauthorized" {
		return ErrUnauthorized
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("phone is %s", state)
}

// State is the phone's state as adb sees it: device, offline,
// unauthorized, or an error when adb does not know it.
func (a *ADB) State(ctx context.Context, serial string) (string, error) {
	out, err := a.run(ctx, "-s", serial, "get-state")
	switch {
	case strings.Contains(out, "unauthorized"):
		return "unauthorized", nil
	case strings.Contains(out, "offline"):
		return "offline", nil
	case err != nil:
		return "", err
	}
	return out, nil
}

// TCPIP moves the phone's ADB to TCPPort, which then stays the same until
// the phone restarts.
func (a *ADB) TCPIP(ctx context.Context, serial string) error {
	_, err := a.run(ctx, "-s", serial, "tcpip", TCPPort)
	return err
}

// Forward makes a local TCP port lead to the phone's abstract socket name
// and returns the port.
func (a *ADB) Forward(ctx context.Context, serial, name string) (int, error) {
	out, err := a.run(ctx, "-s", serial, "forward", "tcp:0", "localabstract:"+name)
	if err != nil {
		return 0, err
	}
	port, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return 0, fmt.Errorf("adb forward: unexpected answer %q", out)
	}
	return port, nil
}

// RemoveForward undoes Forward.
func (a *ADB) RemoveForward(ctx context.Context, serial string, port int) {
	a.run(ctx, "-s", serial, "forward", "--remove", "tcp:"+strconv.Itoa(port))
}

// Push copies a file to the phone.
func (a *ADB) Push(ctx context.Context, serial, local, remote string) error {
	_, err := a.run(ctx, "-s", serial, "push", local, remote)
	return err
}

// Screenshot returns the phone's screen as a PNG at full resolution. The
// windows of apps that block screen capture come out black.
func (a *ADB) Screenshot(ctx context.Context, serial string) ([]byte, error) {
	var out, errOut bytes.Buffer
	c := a.command(ctx, "-s", serial, "exec-out", "screencap", "-p")
	c.Stdout, c.Stderr = &out, &errOut
	if err := c.Run(); err != nil {
		if msg := strings.TrimSpace(errOut.String()); msg != "" {
			return nil, fmt.Errorf("screenshot: %s", msg)
		}
		return nil, fmt.Errorf("screenshot: %w", err)
	}
	png := out.Bytes()
	if !bytes.HasPrefix(png, []byte("\x89PNG")) {
		return nil, errors.New("screenshot: the phone did not send a picture")
	}
	return png, nil
}

// Link connects to the phone at ip: on TCPPort when it was moved there
// since it last restarted, else on the wireless debugging port it last
// used. It returns the ADB serial to use.
func (a *ADB) Link(ctx context.Context, ip, lastPort string) (string, error) {
	addr := net.JoinHostPort(ip, TCPPort)
	err := a.Connect(ctx, addr)
	if err == nil || errors.Is(err, ErrUnauthorized) {
		return addr, err
	}
	if lastPort == "" || lastPort == TCPPort {
		return "", err
	}
	return a.LinkPort(ctx, ip, lastPort)
}

// LinkPort connects to wireless debugging's port and moves the phone to
// TCPPort, so later connections need no port typed in. It returns the
// serial that works.
func (a *ADB) LinkPort(ctx context.Context, ip, port string) (string, error) {
	addr := net.JoinHostPort(ip, port)
	if err := a.Connect(ctx, addr); err != nil {
		return "", err
	}
	if err := a.TCPIP(ctx, addr); err != nil {
		return addr, nil // the wireless debugging port still works
	}
	fixed := net.JoinHostPort(ip, TCPPort)
	// ADB on the phone restarts on the new port.
	for range 10 {
		time.Sleep(700 * time.Millisecond)
		if err := a.Connect(ctx, fixed); err == nil {
			return fixed, nil
		} else if errors.Is(err, ErrUnauthorized) {
			return fixed, err
		}
	}
	return "", fmt.Errorf("the phone did not come back on port %s; turn Wireless debugging off and on, then try again", TCPPort)
}

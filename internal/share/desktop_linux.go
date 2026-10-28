//go:build linux

package share

import (
	"bufio"
	"errors"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

func userHome() (string, error) { return os.UserHomeDir() }

// DownloadDir is the user's Downloads folder (XDG_DOWNLOAD_DIR).
func DownloadDir() string {
	home, _ := os.UserHomeDir()
	if out, err := exec.Command("xdg-user-dir", "DOWNLOAD").Output(); err == nil {
		if d := strings.TrimSpace(string(out)); d != "" && d != home {
			return d
		}
	}
	return filepath.Join(home, "Downloads")
}

// Notify shows n with notify-send and runs the action the user clicks.
func Notify(n Notice) {
	args := []string{"--app-name=Remoter", "--icon=" + iconFor(n)}
	if len(n.Actions) > 0 {
		// --wait keeps notify-send running until the notification is
		// clicked or closed and prints the chosen action.
		args = append(args, "--wait")
		for i, a := range n.Actions {
			args = append(args, "--action="+strconv.Itoa(i)+"="+a.Label)
		}
		// Clicking the notification itself picks the first action.
		args = append(args, "--action=default="+n.Actions[0].Label)
	}
	args = append(args, "--", n.Title, n.Body)
	cmd := exec.Command("notify-send", args...)
	out, err := cmd.Output()
	if err != nil && len(out) == 0 {
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(n.Actions) > 0 {
			// Older notify-send without actions: show it plainly.
			exec.Command("notify-send", "--app-name=Remoter", "--icon="+iconFor(n), "--", n.Title, n.Body).Run()
			return
		}
		log.Printf("share: cannot show a notification (%v): %s: %s", err, n.Title, n.Body)
		return
	}
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	for sc.Scan() {
		id := strings.TrimSpace(sc.Text())
		if id == "default" {
			id = "0"
		}
		if i, err := strconv.Atoi(id); err == nil && i >= 0 && i < len(n.Actions) {
			n.Actions[i].Run()
			return
		}
	}
}

func iconFor(n Notice) string {
	if n.Image != "" {
		return n.Image
	}
	return "smartphone"
}

// CopyText puts text on the Wayland (or X11) clipboard.
func CopyText(text string) error {
	cmd := desktopCommand("wl-copy")
	switch {
	case wayland(cmd) && have("wl-copy"):
	case have("xclip"):
		cmd = desktopCommand("xclip", "-selection", "clipboard")
	default:
		return errors.New("install wl-clipboard to share the clipboard")
	}
	cmd.Stdin = strings.NewReader(text)
	return cmd.Run()
}

// ReadClipboard returns the clipboard text.
func ReadClipboard() (string, error) {
	cmd := desktopCommand("wl-paste", "--no-newline", "--type", "text")
	switch {
	case wayland(cmd) && have("wl-paste"):
	case have("xclip"):
		cmd = desktopCommand("xclip", "-selection", "clipboard", "-o")
	default:
		return "", errors.New("install wl-clipboard to share the clipboard")
	}
	out, err := cmd.Output()
	return string(out), err
}

// Open opens a file or link with its default app.
func Open(target string) {
	if err := desktopCommand("xdg-open", target).Start(); err != nil {
		log.Printf("share: cannot open %s: %v", target, err)
	}
}

// ShowInFolder opens the file manager on path's folder, with the file
// selected where the file manager supports that.
func ShowInFolder(path string) {
	err := desktopCommand("dbus-send", "--session", "--type=method_call", "--dest=org.freedesktop.FileManager1",
		"/org/freedesktop/FileManager1", "org.freedesktop.FileManager1.ShowItems",
		"array:string:file://"+path, "string:").Run()
	if err != nil {
		Open(filepath.Dir(path))
	}
}

// desktopCommand runs name with the desktop session's variables. The
// service may have started before the compositor exported them, so missing
// ones are taken from the systemd user manager each time.
func desktopCommand(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	if os.Getenv("WAYLAND_DISPLAY") != "" || os.Getenv("DISPLAY") != "" {
		return cmd
	}
	out, err := exec.Command("systemctl", "--user", "show-environment").Output()
	if err != nil {
		return cmd
	}
	env := os.Environ()
	for _, line := range strings.Split(string(out), "\n") {
		k, _, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch k {
		case "WAYLAND_DISPLAY", "DISPLAY", "XDG_CURRENT_DESKTOP", "HYPRLAND_INSTANCE_SIGNATURE", "XAUTHORITY":
			env = append(env, line)
		}
	}
	cmd.Env = env
	return cmd
}

func wayland(cmd *exec.Cmd) bool {
	for _, kv := range cmd.Environ() {
		if strings.HasPrefix(kv, "WAYLAND_DISPLAY=") && len(kv) > len("WAYLAND_DISPLAY=") {
			return true
		}
	}
	return false
}

func have(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

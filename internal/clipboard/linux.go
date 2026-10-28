//go:build linux

package clipboard

import (
	"bytes"
	"errors"
	"hash/fnv"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/aleynikovich/remoter/internal/wayland"
)

// commandClipboard uses wl-clipboard on Wayland and xclip or xsel on X11.
type commandClipboard struct {
	read  []string
	write []string
	env   []string

	mu    sync.Mutex
	seq   uint32
	hash  uint64
	asked time.Time
}

func newNative() Clipboard {
	var env []string
	if os.Getenv("WAYLAND_DISPLAY") == "" {
		// A background service: point the tools at the running session.
		if p, err := wayland.SocketPath(); err == nil {
			env = append(os.Environ(), "WAYLAND_DISPLAY="+p)
		}
	}
	waylandSession := os.Getenv("WAYLAND_DISPLAY") != "" || env != nil
	for _, c := range []struct {
		wayland     bool
		read, write []string
	}{
		{true, []string{"wl-paste", "--no-newline", "--type", "text"}, []string{"wl-copy", "--type", "text/plain;charset=utf-8"}},
		{false, []string{"xclip", "-selection", "clipboard", "-o"}, []string{"xclip", "-selection", "clipboard", "-i"}},
		{false, []string{"xsel", "--clipboard", "--output"}, []string{"xsel", "--clipboard", "--input"}},
	} {
		if c.wayland != waylandSession {
			continue
		}
		if _, err := exec.LookPath(c.read[0]); err == nil {
			return &commandClipboard{read: c.read, write: c.write, env: env}
		}
	}
	return nil
}

func (c *commandClipboard) command(args []string) *exec.Cmd {
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Env = c.env
	return cmd
}

// Seq reads the clipboard at most twice a second and counts changes.
func (c *commandClipboard) Seq() uint32 {
	c.mu.Lock()
	defer c.mu.Unlock()
	if time.Since(c.asked) < 500*time.Millisecond {
		return c.seq
	}
	c.asked = time.Now()
	text, err := c.readText()
	if err != nil {
		return c.seq
	}
	h := fnv.New64a()
	h.Write([]byte(text))
	if sum := h.Sum64(); sum != c.hash {
		c.hash = sum
		c.seq++
	}
	return c.seq
}

func (c *commandClipboard) ReadText() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.readText()
}

func (c *commandClipboard) readText() (string, error) {
	cmd := c.command(c.read)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Start(); err != nil {
		return "", err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			// An empty clipboard makes wl-paste fail; that is not an error.
			return "", nil
		}
	case <-time.After(2 * time.Second):
		cmd.Process.Kill()
		return "", errors.New("clipboard: " + c.read[0] + " did not answer")
	}
	if out.Len() > MaxText {
		return "", errors.New("clipboard: too large")
	}
	return toLF(out.String()), nil
}

func (c *commandClipboard) WriteText(s string) error {
	cmd := c.command(c.write)
	cmd.Stdin = bytes.NewReader([]byte(s))
	// wl-copy and xclip stay behind to serve the selection; do not wait.
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	c.mu.Lock()
	h := fnv.New64a()
	h.Write([]byte(s))
	c.hash = h.Sum64()
	c.seq++
	c.mu.Unlock()
	return nil
}

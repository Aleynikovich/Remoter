package phone

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
)

// CopyImage puts a PNG on the clipboard, with wl-copy on Wayland or xclip
// on X11, which keep serving it after this returns.
func CopyImage(png []byte) error {
	var c *exec.Cmd
	switch {
	case os.Getenv("WAYLAND_DISPLAY") != "":
		if _, err := exec.LookPath("wl-copy"); err != nil {
			return errors.New("install wl-clipboard to copy screenshots")
		}
		c = exec.Command("wl-copy", "--type", "image/png")
	default:
		if _, err := exec.LookPath("xclip"); err != nil {
			return errors.New("install xclip to copy screenshots")
		}
		c = exec.Command("xclip", "-selection", "clipboard", "-t", "image/png", "-i")
	}
	c.Stdin = bytes.NewReader(png)
	return c.Run()
}

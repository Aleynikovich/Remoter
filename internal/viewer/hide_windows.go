package viewer

import (
	"os/exec"
	"syscall"
)

// hideWindow keeps a console program started from the windowless viewer
// from flashing a console window.
func hideWindow(c *exec.Cmd) {
	const createNoWindow = 0x08000000
	c.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
}

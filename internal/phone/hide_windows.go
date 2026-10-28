package phone

import (
	"os/exec"
	"syscall"
)

// hideWindow keeps adb, a console program, from flashing a console window
// when the windowless viewer starts it.
func hideWindow(c *exec.Cmd) {
	const createNoWindow = 0x08000000
	c.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
}

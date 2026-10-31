package update

import (
	"os/exec"
	"syscall"
)

const createNoWindow = 0x08000000

// HideWindow keeps cmd from opening a console window, which Windows would
// otherwise do for a console program started by one running in the
// background.
func HideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow, HideWindow: true}
}

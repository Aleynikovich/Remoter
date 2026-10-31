//go:build !windows

package update

import "os/exec"

// HideWindow does nothing outside Windows.
func HideWindow(cmd *exec.Cmd) {}

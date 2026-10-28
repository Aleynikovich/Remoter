//go:build !windows

package phone

import "os/exec"

func hideWindow(*exec.Cmd) {}

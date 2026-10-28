//go:build !windows

package viewer

import "os/exec"

func hideWindow(*exec.Cmd) {}

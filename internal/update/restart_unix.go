//go:build !windows

package update

import (
	"os"
	"syscall"
)

// Restart replaces the running process with exe, keeping its arguments and
// environment. It only returns on failure.
func Restart(exe string, env []string) error {
	return syscall.Exec(exe, os.Args, env)
}

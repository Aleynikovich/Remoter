//go:build !windows

package main

import (
	"errors"
	"os"
	"path/filepath"
)

// Elsewhere Remoter runs in the terminal it was started from; use the
// system's service manager to keep it running.
const backgroundSupported = false

func startDetached(exe string, args, env []string) error {
	return errors.New("not supported on this system")
}

func setAutostart(on bool, exe string, args []string) error { return nil }

func logPath() string {
	return filepath.Join(os.TempDir(), "remoter.log")
}

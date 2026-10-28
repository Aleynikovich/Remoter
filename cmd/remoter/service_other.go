//go:build !windows

package main

import "errors"

// The boot service is a Windows feature. Elsewhere Remoter is kept running
// by the system's own service manager, so these are no-ops.

const serviceSupported = false

func runService() error       { return errors.New("the service is only supported on Windows") }
func installService() error   { return errors.New("the service is only supported on Windows") }
func uninstallService() error { return errors.New("the service is only supported on Windows") }
func serviceRunning() bool    { return false }
func ensureService()          {}

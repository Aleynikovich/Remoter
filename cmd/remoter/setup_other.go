//go:build !windows

package main

import "errors"

// The installer is a Windows thing; elsewhere Remoter is a single file.

func redirectToInstalled()       {}
func runSetup() error            { return errors.New("the installer is only supported on Windows") }
func runUnsetup() error          { return errors.New("the installer is only supported on Windows") }
func setUninstallVersion(string) {}

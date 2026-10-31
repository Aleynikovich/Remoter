//go:build !windows

package main

import "errors"

func isAdmin() bool            { return false }
func canElevateSilently() bool { return false }

func startElevated(string, []string) error { return errors.New("not supported on this system") }

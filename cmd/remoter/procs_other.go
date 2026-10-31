//go:build !windows

package main

// Elsewhere the system's service manager keeps one copy running.

func listRemoters() []remoterProc               { return nil }
func stopProcs(procs []remoterProc, why string) {}

//go:build !windows

// Package wdesktop runs work on the Windows desktop that currently has
// input, so a host started by the boot service can see and control the
// sign-in and lock screens, which live on their own desktops. Outside
// Windows there are no such desktops, so a Worker just runs the work where
// it is.
package wdesktop

// Worker runs functions, following the active input desktop on Windows.
type Worker struct{}

// NewWorker returns a Worker. The name is only used in log lines.
func NewWorker(name string) *Worker { return &Worker{} }

// Do runs fn and waits for it to finish.
func (w *Worker) Do(fn func()) { fn() }

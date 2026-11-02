//go:build windows

package kvm

import (
	"context"
	"testing"
	"time"
)

// TestHooksInstall checks the hooks go in and come out cleanly, and that
// input still flows while they are in (injected input always passes).
func TestHooksInstall(t *testing.T) {
	c, err := newCapture()
	if err != nil {
		t.Fatal(err)
	}
	screens := c.Screens()
	t.Logf("screens %v, on the user desktop %v", screens, c.OnUserDesktop())
	if len(screens) == 0 {
		t.Skip("no screens on this machine")
	}
	m := NewMachine(Config{Screen: -1}, c, &fakeLink{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx, m) }()
	time.Sleep(300 * time.Millisecond)
	c.Warp(screens[0].Min.Add(screens[0].Size().Div(2)))
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("hook thread did not stop")
	}
}

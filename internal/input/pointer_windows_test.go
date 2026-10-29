//go:build windows && (amd64 || arm64)

package input

import (
	"image"
	"testing"
)

// TestPointerLands moves the real pointer, so it only runs where there is
// an interactive desktop (GitHub's Windows runners have one).
func TestPointerLands(t *testing.T) {
	vw, vh := metric(smCXVirtualScreen), metric(smCYVirtualScreen)
	vx, vy := metric(smXVirtualScreen), metric(smYVirtualScreen)
	if vw < 100 || vh < 100 {
		t.Skip("no desktop")
	}
	if ok, _, _ := procSetCursorPos.Call(uintptr(vx+1), uintptr(vy+1)); ok == 0 {
		t.Skip("no interactive desktop")
	}
	for _, p := range []image.Point{
		{vx + 10, vy + 10},
		{vx + vw/2, vy + vh/3},
		{vx + vw - 10, vy + vh - 10},
		{vx + vw/2 + 1, vy + vh/3},
	} {
		moveTo(p)
		if got, _ := cursorPos(); got != p {
			t.Errorf("pointer at %v, want %v", got, p)
		}
	}
}

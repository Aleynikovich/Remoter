//go:build windows && (amd64 || arm64)

package input

import (
	"image"
	"testing"
)

// TestPointerLands moves the real pointer, so it only runs where there is
// an interactive desktop (GitHub's Windows runners have one).
func TestPointerLands(t *testing.T) {
	_, m := monitorAt(image.Pt(0, 0))
	if m.Empty() {
		t.Skip("no monitor")
	}
	if ok, _, _ := procSetCursorPos.Call(uintptr(m.Min.X+1), uintptr(m.Min.Y+1)); ok == 0 {
		t.Skip("no interactive desktop")
	}
	var pt pointer
	for _, p := range []image.Point{
		m.Min.Add(image.Pt(10, 10)),
		m.Min.Add(image.Pt(m.Dx()/2, m.Dy()/3)),
		m.Max.Sub(image.Pt(10, 10)),
	} {
		pt.move(p, true)
		if got, _ := cursorPos(); got != p {
			t.Errorf("pointer at %v, want %v", got, p)
		}
	}
	g := pt.grids[func() uintptr { h, _ := monitorAt(m.Min); return h }()]
	t.Logf("monitor %v, grid measured %v", m, g != nil && g.ok)
}

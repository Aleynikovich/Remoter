//go:build windows && (amd64 || arm64)

package input

import (
	"image"
	"time"
)

// moveTo puts the pointer on p with a relative SendInput move from where it
// is now, then makes sure it landed there.
//
// Relative moves are what a real mouse sends. Games read raw mouse input
// and ignore absolute moves and SetCursorPos, so hovering over things
// (monsters in Dofus) showed nothing. Pointer speed and acceleration
// scale relative moves, so the move may stop short or overshoot; then
// SetCursorPos puts the pointer on the exact pixel, which also beats
// SendInput's absolute grid, which put the pointer far to the right on
// Aley's two monitors.
func moveTo(p image.Point) {
	from, ok := cursorPos()
	if !ok {
		setCursor(p)
		return
	}
	if from == p {
		return
	}
	sendMouse(int32(p.X-from.X), int32(p.Y-from.Y), 0, mouseeventfMove)
	if got, _ := landed(from, 10*time.Millisecond); got != p {
		setCursor(p)
	}
}

// landed waits until the pointer has left from, and returns where it is.
// Injected input is handled on another thread, so a move shows up a
// moment after SendInput returns.
func landed(from image.Point, wait time.Duration) (image.Point, bool) {
	deadline := time.Now().Add(wait)
	for {
		if p, ok := cursorPos(); !ok || p != from {
			return p, ok
		}
		if time.Now().After(deadline) {
			return from, false
		}
		time.Sleep(200 * time.Microsecond)
	}
}

// setCursor places the pointer with SetCursorPos, or with SendInput's
// absolute grid where that is refused (the secure desktop).
func setCursor(p image.Point) {
	if ok, _, _ := procSetCursorPos.Call(uintptr(p.X), uintptr(p.Y)); ok != 0 {
		return
	}
	vx, vy := metric(smXVirtualScreen), metric(smYVirtualScreen)
	vw, vh := metric(smCXVirtualScreen), metric(smCYVirtualScreen)
	if vw <= 1 || vh <= 1 {
		return
	}
	nx := int32((int64(p.X-vx)*65535 + int64(vw-1)/2) / int64(vw-1))
	ny := int32((int64(p.Y-vy)*65535 + int64(vh-1)/2) / int64(vh-1))
	sendMouse(nx, ny, 0, mouseeventfMove|mouseeventfAbsolute|mouseeventfVirtualDsk)
}

// Package input applies remote mouse and keyboard events on the host.
package input

import (
	"image"
	"log"

	"github.com/aleynikovich/remoter/internal/protocol"
)

// Handler applies input events. It matches host.InputHandler.
type Handler interface {
	HandleInput(viewer string, in protocol.Input)
	// ReleaseAll lets go of every key and button a viewer still holds, so a
	// dropped connection cannot leave Ctrl or the mouse button stuck down.
	ReleaseAll(viewer string)
	// SetDisplay moves input to another screen area, after the viewer
	// switches monitors.
	SetDisplay(display image.Rectangle)
}

// New returns the native injector for this platform, applying events to the
// given screen area. It returns an error where injection is not implemented.
func New(display image.Rectangle) (Handler, error) {
	return newNative(display)
}

// Log only logs events. It is used for view-only sessions and on platforms
// without an injector.
type Log struct{}

func (Log) HandleInput(viewer string, in protocol.Input) {
	log.Printf("input from viewer %s (not applied): %+v", viewer, in)
}

func (Log) ReleaseAll(string) {}

func (Log) SetDisplay(image.Rectangle) {}

// toScreen maps normalised 0..1 coordinates to a pixel inside display.
func toScreen(display image.Rectangle, x, y float64) image.Point {
	clamp := func(v float64) float64 { return min(max(v, 0), 1) }
	px := display.Min.X + int(clamp(x)*float64(display.Dx()-1)+0.5)
	py := display.Min.Y + int(clamp(y)*float64(display.Dy()-1)+0.5)
	return image.Pt(px, py)
}

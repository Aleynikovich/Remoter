// Package cursor reports the shape of the host's mouse pointer, so the viewer
// can show the same pointer locally without waiting for screen updates.
package cursor

import "image"

// Shape returns the current pointer as a CSS cursor name: "default", "text",
// "pointer" and so on. It returns "" where this is not
// supported, and "default" for shapes it does not recognise.
func Shape() string {
	return shape()
}

// Draw returns img with the host's mouse pointer drawn on it, for viewers
// that want to see where the real pointer is. origin is where img's
// top-left pixel sits on the desktop. When the pointer is hidden or not
// on img, or where this is not supported, it returns img itself;
// otherwise a copy, so the capturer's own buffers stay clean.
func Draw(img image.Image, origin image.Point) (out image.Image) {
	// A crash here would cut the PC off until someone restarts Remoter,
	// so a bad pointer picture only costs the pointer.
	defer func() {
		if recover() != nil {
			out = img
		}
	}()
	return draw(img, origin)
}

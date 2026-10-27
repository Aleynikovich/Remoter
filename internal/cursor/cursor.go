// Package cursor reports the shape of the host's mouse pointer, so the viewer
// can show the same pointer locally without waiting for screen updates.
package cursor

// Shape returns the current pointer as a CSS cursor name: "default", "text",
// "pointer" and so on. It returns "" where this is not
// supported, and "default" for shapes it does not recognise.
func Shape() string {
	return shape()
}

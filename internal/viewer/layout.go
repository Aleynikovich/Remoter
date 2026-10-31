package viewer

import "image"

// Fit returns where a w×h picture goes inside an out-sized area: as large as
// fits without changing its shape, centred, with black bars on the sides
// that do not fill.
func Fit(w, h int, out image.Point) image.Rectangle {
	if w <= 0 || h <= 0 || out.X <= 0 || out.Y <= 0 {
		return image.Rectangle{}
	}
	dw, dh := out.X, out.X*h/w
	if dh > out.Y {
		dw, dh = out.Y*w/h, out.Y
	}
	x, y := (out.X-dw)/2, (out.Y-dh)/2
	return image.Rect(x, y, x+dw, y+dh)
}

// Normalize maps a point in the same space as dst to 0..1 of the remote
// screen, which is how input coordinates travel. Points on the black bars
// are clamped to the nearest edge.
func Normalize(dst image.Rectangle, x, y float64) (nx, ny float64) {
	if dst.Empty() {
		return 0, 0
	}
	clamp := func(v float64) float64 { return min(max(v, 0), 1) }
	nx = (x - float64(dst.Min.X)) / float64(dst.Dx())
	ny = (y - float64(dst.Min.Y)) / float64(dst.Dy())
	return clamp(nx), clamp(ny)
}

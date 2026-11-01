package capture

import (
	"image"
	"image/color"
	"testing"

	"github.com/aleynikovich/remoter/internal/protocol"
)

func TestCropTo(t *testing.T) {
	// Columns numbered by their x, on a display that is not at 0,0.
	b := image.Rect(-5120, 100, 0, 1540)
	img := image.NewRGBA(b)
	for x := b.Min.X; x < b.Max.X; x++ {
		for y := b.Min.Y; y < b.Max.Y; y += 97 {
			img.Set(x, y, color.RGBA{uint8(x), uint8(x >> 8), 0, 255})
		}
	}
	var s Screen
	r := &protocol.Region{X: 0.55, W: 0.45, H: 1}
	got, off := s.cropTo(img, r)
	if off != image.Pt(2816, 0) {
		t.Errorf("offset %v", off)
	}
	sub := got.(*image.RGBA)
	if sub.Rect.Dx() != 2304 || sub.Rect.Dy() != 1440 {
		t.Fatalf("size %v", sub.Rect)
	}
	// What the encoders read: rows from Pix[0] with Stride.
	px := sub.Pix[0:4]
	want := -5120 + 2816
	if px[0] != uint8(want) || px[1] != uint8(want>>8) {
		t.Errorf("first pixel %v, want column %d", px, want)
	}
	// Not RGBA: copied.
	gray := image.NewGray(image.Rect(0, 0, 100, 50))
	gray.Pix[30] = 200
	g, off := s.cropTo(gray, &protocol.Region{X: 0.3, W: 0.4, H: 1})
	if off != image.Pt(30, 0) || g.Bounds().Dx() != 40 {
		t.Fatalf("gray crop %v %v", g.Bounds(), off)
	}
	if r, _, _, _ := g.At(g.Bounds().Min.X, g.Bounds().Min.Y).RGBA(); r>>8 != 200 {
		t.Errorf("gray crop starts at the wrong pixel: %d", r>>8)
	}
	// Odd sizes become even, for video.
	o, _ := s.cropTo(image.NewRGBA(image.Rect(0, 0, 1001, 501)), &protocol.Region{X: 0.1, W: 0.333, H: 0.777})
	if o.Bounds().Dx()%2 != 0 || o.Bounds().Dy()%2 != 0 {
		t.Errorf("odd crop %v", o.Bounds())
	}
}

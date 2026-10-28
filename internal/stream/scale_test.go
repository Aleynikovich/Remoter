package stream

import (
	"image"
	"image/color"
	"testing"
)

func TestDownscaleAverages(t *testing.T) {
	src := image.NewRGBA(image.Rect(10, 10, 14, 12)) // offset like a second monitor
	for x := 10; x < 14; x++ {
		v := uint8(0)
		if x%2 == 1 {
			v = 200
		}
		src.Set(x, 10, color.RGBA{v, v, v, 255})
		src.Set(x, 11, color.RGBA{v, v, v, 255})
	}
	dst := Downscale(nil, src, 2, 1)
	for x := range 2 {
		if c := dst.RGBAAt(x, 0); c.R != 100 || c.A != 255 {
			t.Fatalf("pixel %d = %v, want 100", x, c)
		}
	}
	if again := Downscale(dst, src, 2, 1); again != dst {
		t.Fatal("same size should reuse the image")
	}
}

func BenchmarkDownscale3200To2400(b *testing.B) {
	src := image.NewRGBA(image.Rect(0, 0, 3200, 1800))
	var dst *image.RGBA
	for b.Loop() {
		dst = Downscale(dst, src, 2400, 1350)
	}
}

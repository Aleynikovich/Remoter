package mf

import (
	"image"
	"image/color"
	"testing"
)

func TestNV12Colours(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 4, 2))
	for x := range 4 {
		for y := range 2 {
			img.Set(x, y, []color.RGBA{{255, 255, 255, 255}, {0, 0, 0, 255}}[x/2])
		}
	}
	dst := make([]byte, 4*2*3/2)
	toNV12(dst, img, 4, 2)
	// White is Y 235, black Y 16; both have neutral chroma 128.
	want := []byte{235, 235, 16, 16, 235, 235, 16, 16, 128, 128, 128, 128}
	for i := range want {
		if d := int(dst[i]) - int(want[i]); d < -1 || d > 1 {
			t.Fatalf("NV12 = %v, want %v", dst, want)
		}
	}
}

func TestNALTypes(t *testing.T) {
	idr, sps := nalTypes([]byte{0, 0, 0, 1, 0x67, 1, 2, 0, 0, 1, 0x68, 3, 0, 0, 1, 0x65, 4})
	if !idr || !sps {
		t.Fatal("missed the SPS or IDR slice")
	}
	if idr, sps := nalTypes([]byte{0, 0, 1, 0x41, 9}); idr || sps {
		t.Fatal("found an IDR slice or SPS in a P frame")
	}
}

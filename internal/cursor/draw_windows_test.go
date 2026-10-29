//go:build windows

package cursor

import (
	"image"
	"testing"
)

func TestLoadSystemPointers(t *testing.T) {
	for id, name := range map[uintptr]string{32512: "arrow", 32513: "I-beam", 32649: "hand"} {
		h, _, _ := procLoadCursorW.Call(0, id)
		if h == 0 {
			t.Fatalf("LoadCursor %s failed", name)
		}
		pic := load(h)
		if pic == nil {
			t.Fatalf("%s: no picture", name)
		}
		drawn := 0
		for k := 0; k < pic.w*pic.h; k++ {
			if pic.invert[k] || pic.bgra[4*k+3] != 0 {
				drawn++
			}
		}
		if drawn == 0 {
			t.Errorf("%s: %dx%d picture is empty", name, pic.w, pic.h)
		}
		t.Logf("%s: %dx%d, hot spot %d,%d, %d pixels drawn", name, pic.w, pic.h, pic.hotX, pic.hotY, drawn)
	}
}

func TestDrawLeavesSourceAlone(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 2048, 2048))
	out := Draw(src, src.Rect.Min)
	for _, v := range src.Pix {
		if v != 0 {
			t.Fatal("Draw changed the captured image")
		}
	}
	t.Logf("pointer drawn: %v", out != image.Image(src))
}

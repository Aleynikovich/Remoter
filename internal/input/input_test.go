package input

import (
	"image"
	"testing"
)

func TestVirtualKey(t *testing.T) {
	for _, tc := range []struct {
		code     string
		vk       uint16
		extended bool
	}{
		{"KeyA", 0x41, false},
		{"KeyZ", 0x5A, false},
		{"Digit0", 0x30, false},
		{"Numpad7", 0x67, false},
		{"F1", 0x70, false},
		{"F12", 0x7B, false},
		{"F24", 0x87, false},
		{"Enter", 0x0D, false},
		{"NumpadEnter", 0x0D, true},
		{"ArrowUp", 0x26, true},
		{"ControlLeft", 0xA2, false},
		{"ControlRight", 0xA3, true},
		{"MetaLeft", 0x5B, true},
	} {
		vk, ext, ok := virtualKey(tc.code)
		if !ok || vk != tc.vk || ext != tc.extended {
			t.Errorf("virtualKey(%q) = %#x, %v, %v; want %#x, %v", tc.code, vk, ext, ok, tc.vk, tc.extended)
		}
	}
	if _, _, ok := virtualKey("Unidentified"); ok {
		t.Error("unknown code mapped")
	}
}

func TestToScreen(t *testing.T) {
	// Second monitor to the left of the primary one.
	d := image.Rect(-1920, 0, 0, 1080)
	for _, tc := range []struct {
		x, y float64
		want image.Point
	}{
		{0, 0, image.Pt(-1920, 0)},
		{1, 1, image.Pt(-1, 1079)},
		{0.5, 0.5, image.Pt(-960, 540)},
		{-3, 7, image.Pt(-1920, 1079)},
	} {
		if got := toScreen(d, tc.x, tc.y); got != tc.want {
			t.Errorf("toScreen(%v, %v) = %v, want %v", tc.x, tc.y, got, tc.want)
		}
	}
}

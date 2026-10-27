package viewer

import (
	"image"
	"path/filepath"
	"testing"
)

func TestViewerURL(t *testing.T) {
	for in, want := range map[string]string{
		"100.102.208.35:8080":       "ws://100.102.208.35:8080/ws/viewer",
		" http://relay:8080/ ":      "ws://relay:8080/ws/viewer",
		"https://relay.example":     "wss://relay.example/ws/viewer",
		"wss://relay/custom/viewer": "wss://relay/custom/viewer",
	} {
		got, err := ViewerURL(in)
		if err != nil || got != want {
			t.Errorf("ViewerURL(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "ftp://relay"} {
		if _, err := ViewerURL(in); err == nil {
			t.Errorf("ViewerURL(%q) succeeded", in)
		}
	}
}

func TestCodeForScancode(t *testing.T) {
	for sc, want := range map[int]string{
		4: "KeyA", 29: "KeyZ", 30: "Digit1", 39: "Digit0", 40: "Enter",
		58: "F1", 69: "F12", 104: "F13", 98: "Numpad0", 97: "Numpad9",
		80: "ArrowLeft", 227: "MetaLeft", 230: "AltRight", 0: "", 300: "",
	} {
		if got := CodeForScancode(sc); got != want {
			t.Errorf("CodeForScancode(%d) = %q, want %q", sc, got, want)
		}
	}
}

func TestFitAndNormalize(t *testing.T) {
	// A 16:9 screen in a square window gets bars above and below.
	dst := Fit(1920, 1080, image.Pt(1000, 1000))
	if want := image.Rect(0, 219, 1000, 781); dst != want {
		t.Fatalf("Fit = %v, want %v", dst, want)
	}
	if x, y := Normalize(dst, 500, 500); x != 0.5 || y != 0.5 {
		t.Errorf("centre maps to %v,%v", x, y)
	}
	if x, y := Normalize(dst, -5, 10); x != 0 || y != 0 {
		t.Errorf("outside point maps to %v,%v, want 0,0", x, y)
	}
	if dst := Fit(1080, 1920, image.Pt(1000, 1000)); dst != image.Rect(219, 0, 781, 1000) {
		t.Errorf("portrait Fit = %v", dst)
	}
}

func TestConfigRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "remoter", "viewer.json")
	if c := LoadConfig(path); c != (Config{}) {
		t.Fatalf("missing file gave %+v", c)
	}
	want := Config{Server: "relay:8080", Password: "root"}
	if err := want.Save(path); err != nil {
		t.Fatal(err)
	}
	if got := LoadConfig(path); got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

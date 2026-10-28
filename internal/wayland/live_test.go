//go:build linux

package wayland

import (
	"image/png"
	"os"
	"testing"
)

// TestLive captures the screen of a running wlroots compositor. It needs
// REMOTER_WAYLAND_TEST=1 and a session, so it is skipped in CI.
func TestLive(t *testing.T) {
	if os.Getenv("REMOTER_WAYLAND_TEST") == "" {
		t.Skip("set REMOTER_WAYLAND_TEST=1 with a wlroots session running")
	}
	c, err := Connect()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	outs := c.Outputs()
	if len(outs) == 0 {
		t.Fatal("no outputs")
	}
	t.Logf("outputs: %+v", outs)
	cp, err := c.NewCopier(outs[0])
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		img, err := cp.Capture()
		if err != nil {
			t.Fatal(err)
		}
		if img.Bounds().Dx() != outs[0].W {
			t.Fatalf("captured %v, output is %dx%d", img.Bounds(), outs[0].W, outs[0].H)
		}
		if p := os.Getenv("REMOTER_WAYLAND_PNG"); p != "" {
			f, _ := os.Create(p)
			png.Encode(f, img)
			f.Close()
		}
	}
}

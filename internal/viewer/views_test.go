package viewer

import (
	"math"
	"testing"

	"github.com/aleynikovich/remoter/internal/protocol"
)

func TestPartsFitUltrawide(t *testing.T) {
	// 5120x1440 seen from a 16:10 screen: three 2304x1440 pieces.
	ps := Parts(5120, 1440, 1.6, "fit")
	if len(ps) != 3 {
		t.Fatalf("got %d parts, want 3: %+v", len(ps), ps)
	}
	for i, p := range ps {
		if w := p.W * 5120; math.Abs(w-2304) > 1 || p.H != 1 {
			t.Errorf("part %d is %.0fx%.0f, want 2304x1440", i, w, p.H*1440)
		}
	}
	if ps[0].X != 0 || math.Abs(ps[2].X+ps[2].W-1) > 1e-9 {
		t.Errorf("parts do not reach both edges: %+v", ps)
	}
	if c := ps[1].X + ps[1].W/2; math.Abs(c-0.5) > 1e-9 {
		t.Errorf("centre part is centred at %v", c)
	}
}

func TestPartsFitLeavesSimilarShapes(t *testing.T) {
	for _, c := range []struct{ w, h int }{{1920, 1080}, {2560, 1600}, {3200, 1800}} {
		if ps := Parts(c.w, c.h, 1.6, "fit"); ps != nil {
			t.Errorf("%dx%d split into %+v", c.w, c.h, ps)
		}
	}
	// A phone (20:9) sees an ultrawide as two pieces.
	if ps := Parts(5120, 1440, 20.0/9, "fit"); len(ps) != 2 {
		t.Errorf("phone: %d parts", len(ps))
	}
	// A portrait monitor is cut top to bottom.
	ps := Parts(1440, 5120, 1.6, "fit")
	if len(ps) < 2 || ps[0].W != 1 {
		t.Errorf("portrait: %+v", ps)
	}
}

func TestPartsPresets(t *testing.T) {
	ps := Parts(1920, 1080, 1.6, "thirds")
	if len(ps) != 3 || math.Abs(ps[1].X-1.0/3) > 1e-9 || math.Abs(ps[1].W-1.0/3) > 1e-9 {
		t.Errorf("thirds: %+v", ps)
	}
	if ps := Parts(1920, 1080, 1.6, "halves"); len(ps) != 2 || ps[1].X != 0.5 {
		t.Errorf("halves: %+v", ps)
	}
	if ps := Parts(5120, 1440, 1.6, "off"); ps != nil {
		t.Errorf("off: %+v", ps)
	}
}

func TestViews(t *testing.T) {
	sizes := []protocol.Size{{W: 5120, H: 1440}, {W: 3840, H: 2160}, {W: 8960, H: 2160}}
	areas := []Area{{Server: "pc", Display: 1, W: 3840, H: 2160, Region: protocol.Region{X: 0.1, Y: 0.1, W: 0.5, H: 0.5}},
		{Server: "other", Display: 1, W: 3840, H: 2160}}
	vs := Views(2, true, sizes, 1.6, "fit", areas, "pc")
	var labels []string
	for _, v := range vs {
		labels = append(labels, v.Label)
	}
	want := []string{"Monitor 1", "Monitor 1 · Left", "Monitor 1 · Centre", "Monitor 1 · Right", "Monitor 2", "Monitor 2 · Area 1",
		"All monitors", "All monitors · Left", "All monitors · Centre", "All monitors · Right"}
	if len(labels) != len(want) {
		t.Fatalf("got %q, want %q", labels, want)
	}
	for i := range want {
		if labels[i] != want[i] {
			t.Fatalf("got %q, want %q", labels, want)
		}
	}
	one := Views(1, false, sizes[:1], 1.6, "fit", nil, "pc")
	if len(one) != 4 || one[0].Label != "Whole screen" || one[1].Label != "Left" {
		t.Errorf("one display: %+v", one)
	}
}

func TestZoom(t *testing.T) {
	x0, _, x1, _, ok := Zoom(5120, 1440, 1600, 1000, 0.5, 0.5)
	if !ok || math.Abs(x1-x0-2304) > 1e-6 || math.Abs(x0-(5120-2304)/2.0) > 1e-6 {
		t.Errorf("zoom = %v..%v %v", x0, x1, ok)
	}
	if _, _, _, _, ok := Zoom(1920, 1200, 1600, 1000, 0.5, 0.5); ok {
		t.Error("zoomed a picture that already fits")
	}
}

func TestRegionHelpers(t *testing.T) {
	outer := &protocol.Region{X: 0.5, W: 0.5, H: 1}
	r := Within(outer, protocol.Region{X: 0.5, Y: 0, W: 0.5, H: 0.5})
	if r.X != 0.75 || r.W != 0.25 || r.H != 0.5 {
		t.Errorf("within: %+v", r)
	}
	if !SameRegion(nil, &protocol.Region{W: 1, H: 1}) || SameRegion(nil, outer) {
		t.Error("SameRegion")
	}
	x, y := outer.Map(0.5, 0.5)
	if x != 0.75 || y != 0.5 {
		t.Errorf("map = %v,%v", x, y)
	}
}

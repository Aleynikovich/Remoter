package viewer

import (
	"fmt"
	"math"

	"github.com/aleynikovich/remoter/internal/protocol"
)

// Splits are the ways a display can be cut into virtual monitors: pieces
// shaped like the viewer's screen, two halves, three thirds, or not at all.
var Splits = []string{"fit", "halves", "thirds", "off"}

// SplitLabel names a split for menus.
func SplitLabel(split string) string {
	switch split {
	case "halves":
		return "halves"
	case "thirds":
		return "thirds"
	case "off":
		return "off"
	}
	return "fit my screen"
}

// Parts cuts a w×h display into virtual monitors along its long side, as
// regions of it. viewAspect is the width/height of the viewer's screen,
// which "fit" pieces take: they overlap a little so each fills that screen
// exactly. A display already about the viewer's shape is not cut by "fit".
func Parts(w, h int, viewAspect float64, split string) []protocol.Region {
	if w <= 0 || h <= 0 {
		return nil
	}
	aspect := float64(w) / float64(h)
	wide := aspect >= 1
	if viewAspect > 0 {
		wide = aspect >= viewAspect
	}
	n, size := 0, 0.0 // pieces, and each one's share of the long side
	switch split {
	case "halves", "thirds":
		n = 2
		if split == "thirds" {
			n = 3
		}
		size = 1 / float64(n)
	case "off":
		return nil
	default:
		if viewAspect <= 0 {
			return nil
		}
		r := aspect / viewAspect // how many viewer screens across
		if !wide {
			r = viewAspect / aspect
		}
		if r < 1.3 {
			return nil
		}
		// A little short of a whole extra piece is let off as overlap.
		n = int(math.Ceil(r - 0.15))
		size = 1 / r
	}
	parts := make([]protocol.Region, n)
	for i := range parts {
		start := 0.0
		if n > 1 {
			start = float64(i) * (1 - size) / float64(n-1)
		}
		if wide {
			parts[i] = protocol.Region{X: start, W: size, H: 1}
		} else {
			parts[i] = protocol.Region{Y: start, W: 1, H: size}
		}
	}
	return parts
}

// PartName names piece i of n of a display cut along its width (wide) or
// height.
func PartName(i, n int, wide bool) string {
	names := map[int][]string{2: {"Left", "Right"}, 3: {"Left", "Centre", "Right"}}
	if !wide {
		names = map[int][]string{2: {"Top", "Bottom"}, 3: {"Top", "Middle", "Bottom"}}
	}
	if ns, ok := names[n]; ok {
		return ns[i]
	}
	return fmt.Sprintf("Part %d", i+1)
}

// View is something a host can be asked to show: a display, every display
// (Display == the display count), or a region of either.
type View struct {
	Display int
	Region  *protocol.Region
	Label   string // for menus, e.g. "Monitor 1 · Left"
	Custom  bool   // a region the user drew
}

// Area is a virtual monitor the user drew, kept per computer and per
// display size, so it comes back on the same monitor.
type Area struct {
	Server  string          `json:"server"`
	Display int             `json:"display"`
	W       int             `json:"w"`
	H       int             `json:"h"`
	Region  protocol.Region `json:"region"`
}

// Views lists what a host can show: each display followed by its virtual
// monitors, then every display at once, then the areas drawn for it.
func Views(count int, all bool, sizes []protocol.Size, viewAspect float64, split string, areas []Area, server string) []View {
	var vs []View
	n := count
	if all && count > 1 {
		n++
	}
	for d := range n {
		name := "All monitors"
		if d < count {
			name = fmt.Sprintf("Monitor %d", d+1)
		}
		if count == 1 {
			name = "Whole screen"
		}
		vs = append(vs, View{Display: d, Label: name})
		if d >= len(sizes) {
			continue
		}
		s := sizes[d]
		parts := Parts(s.W, s.H, viewAspect, split)
		wide := len(parts) > 0 && parts[0].H == 1
		prefix := ""
		if count > 1 {
			prefix = name + " · "
		}
		for i := range parts {
			vs = append(vs, View{Display: d, Region: &parts[i], Label: prefix + PartName(i, len(parts), wide)})
		}
		k := 0
		for _, a := range areas {
			if a.Server == server && a.Display == d && a.W == s.W && a.H == s.H {
				k++
				r := a.Region
				vs = append(vs, View{Display: d, Region: &r, Label: fmt.Sprintf("%sArea %d", prefix, k), Custom: true})
			}
		}
	}
	return vs
}

// SameRegion reports whether two regions are the same, nil being the whole
// display.
func SameRegion(a, b *protocol.Region) bool {
	a, b = a.Clean(), b.Clean()
	if a == nil || b == nil {
		return a == b
	}
	near := func(x, y float64) bool { return math.Abs(x-y) < 0.002 }
	return near(a.X, b.X) && near(a.Y, b.Y) && near(a.W, b.W) && near(a.H, b.H)
}

// Within maps a region of a view (r, as 0..1 of what the view shows) to a
// region of the whole display, when the view is itself a region (outer).
func Within(outer *protocol.Region, r protocol.Region) protocol.Region {
	if outer == nil {
		return r
	}
	return protocol.Region{X: outer.X + r.X*outer.W, Y: outer.Y + r.Y*outer.H, W: r.W * outer.W, H: r.H * outer.H}
}

// Zoom returns the part of a w×h picture to show in an out-shaped area so
// it fills it, panned so the point at fraction (fx, fy) of the area sits on
// the same fraction of the picture: moving the pointer across the area
// slides the view across the whole picture. ok is false when the picture
// already has about the area's shape.
func Zoom(w, h int, outW, outH int, fx, fy float64) (x0, y0, x1, y1 float64, ok bool) {
	if w <= 0 || h <= 0 || outW <= 0 || outH <= 0 {
		return 0, 0, float64(w), float64(h), false
	}
	c := func(v float64) float64 { return min(max(v, 0), 1) }
	pic, out := float64(w)/float64(h), float64(outW)/float64(outH)
	sw, sh := float64(w), float64(h)
	switch {
	case pic > out*1.05:
		sw = sh * out
	case out > pic*1.05:
		sh = sw / out
	default:
		return 0, 0, float64(w), float64(h), false
	}
	x0 = c(fx) * (float64(w) - sw)
	y0 = c(fy) * (float64(h) - sh)
	return x0, y0, x0 + sw, y0 + sh, true
}

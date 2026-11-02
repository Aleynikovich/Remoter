// Package kvm is the merged desktop: one mouse and keyboard used on two
// computers. The pointer leaves this computer (the sender, which has the
// devices) at a chosen edge of its screens and goes on across the other
// computer's screens; the keyboard follows the pointer.
//
// The sender decides everything, as Synergy-style tools do: it keeps the
// remote pointer position itself, in the remote layout's coordinates, and
// sends it as absolute positions, so the remote computer needs no edge
// logic and the pointer never drifts. The remote end is an ordinary
// Remoter host that a sender joins without asking for a picture
// (protocol.TypeKVM).
//
// The geometry is worked out as if the remote computer were below this
// one: a Side other than Bottom is turned into that case by a transform,
// so one set of rules covers all four sides.
package kvm

import (
	"fmt"
	"image"
	"math"
	"strings"
)

// Side is the edge of this computer's screens the remote computer is past.
type Side int

const (
	Bottom Side = iota
	Top
	Left
	Right
)

// ParseSide reads "bottom", "top", "left" or "right" (or "below", "above").
func ParseSide(s string) (Side, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "bottom", "below", "down", "":
		return Bottom, nil
	case "top", "above", "up":
		return Top, nil
	case "left":
		return Left, nil
	case "right":
		return Right, nil
	}
	return Bottom, fmt.Errorf("unknown side %q: use bottom, top, left or right", s)
}

func (s Side) String() string {
	return [...]string{"bottom", "top", "left", "right"}[s]
}

// fpoint is a point with fractions, for a pointer moved by scaled deltas.
type fpoint struct{ X, Y float64 }

// transform maps real coordinates to the working space, where the remote
// computer is below. Each one is its own inverse or has an easy one.
type transform Side

// pt maps a pixel.
func (t transform) pt(p image.Point) image.Point {
	switch Side(t) {
	case Top:
		return image.Pt(p.X, -p.Y)
	case Left:
		return image.Pt(p.Y, -p.X)
	case Right:
		return image.Pt(p.Y, p.X)
	}
	return p
}

// inv maps a pixel of the working space back.
func (t transform) inv(p image.Point) image.Point {
	switch Side(t) {
	case Top:
		return image.Pt(p.X, -p.Y)
	case Left:
		return image.Pt(-p.Y, p.X)
	case Right:
		return image.Pt(p.Y, p.X)
	}
	return p
}

// delta maps a movement.
func (t transform) delta(d fpoint) fpoint {
	switch Side(t) {
	case Top:
		return fpoint{d.X, -d.Y}
	case Left:
		return fpoint{d.Y, -d.X}
	case Right:
		return fpoint{d.Y, d.X}
	}
	return d
}

// invF maps a fractional point of the working space back.
func (t transform) invF(p fpoint) fpoint {
	switch Side(t) {
	case Top:
		return fpoint{p.X, -p.Y}
	case Left:
		return fpoint{-p.Y, p.X}
	case Right:
		return fpoint{p.Y, p.X}
	}
	return p
}

// rect maps a rectangle of pixels.
func (t transform) rect(r image.Rectangle) image.Rectangle {
	a, b := t.pt(r.Min), t.pt(r.Max.Sub(image.Pt(1, 1)))
	return image.Rect(min(a.X, b.X), min(a.Y, b.Y), max(a.X, b.X)+1, max(a.Y, b.Y)+1)
}

// segment is part of a local screen's bottom row (working space) with no
// other screen below it, so the pointer can leave there.
type segment struct {
	x0, x1 int // pixels x0..x1-1
	y      int // the bottom row
	screen int // index into the local screens
}

// exitSegments finds where the pointer can leave the local screens (in the
// working space). only, if 0 or more, keeps one screen's edge.
func exitSegments(screens []image.Rectangle, only int) []segment {
	var segs []segment
	for i, m := range screens {
		if only >= 0 && i != only {
			continue
		}
		free := [][2]int{{m.Min.X, m.Max.X}}
		for j, n := range screens {
			if j == i || n.Min.Y != m.Max.Y {
				continue
			}
			free = cut(free, n.Min.X, n.Max.X)
		}
		for _, f := range free {
			if f[1] > f[0] {
				segs = append(segs, segment{x0: f[0], x1: f[1], y: m.Max.Y - 1, screen: i})
			}
		}
	}
	return segs
}

// cut removes x0..x1 from a list of ranges.
func cut(ranges [][2]int, x0, x1 int) [][2]int {
	var out [][2]int
	for _, r := range ranges {
		if x1 <= r[0] || x0 >= r[1] {
			out = append(out, r)
			continue
		}
		if x0 > r[0] {
			out = append(out, [2]int{r[0], x0})
		}
		if x1 < r[1] {
			out = append(out, [2]int{x1, r[1]})
		}
	}
	return out
}

// span is the x range the segments cover together.
func span(segs []segment) (int, int) {
	x0, x1 := math.MaxInt, math.MinInt
	for _, s := range segs {
		x0, x1 = min(x0, s.x0), max(x1, s.x1)
	}
	return x0, x1
}

func box(rs []image.Rectangle) image.Rectangle {
	var u image.Rectangle
	for _, r := range rs {
		u = u.Union(r)
	}
	return u
}

// scale maps v from a0..a1 onto b0..b1, proportionally.
func scale(v, a0, a1, b0, b1 float64) float64 {
	if a1-a0 < 1 {
		return (b0 + b1) / 2
	}
	return b0 + (v-a0)*(b1-b0)/(a1-a0)
}

// geometry is both computers' screens in the working space.
type geometry struct {
	t      transform
	local  []image.Rectangle // this computer's screens
	segs   []segment
	remote []image.Rectangle // the other computer's screens
	rbox   image.Rectangle   // around remote, in real coordinates
}

func newGeometry(side Side, local, remote []image.Rectangle, only int) (*geometry, error) {
	g := &geometry{t: transform(side)}
	for _, r := range local {
		g.local = append(g.local, g.t.rect(r))
	}
	for _, r := range remote {
		if !r.Empty() {
			g.remote = append(g.remote, g.t.rect(r))
			g.rbox = g.rbox.Union(r)
		}
	}
	if len(g.local) == 0 {
		return nil, fmt.Errorf("no screens found on this computer")
	}
	if len(g.remote) == 0 {
		return nil, fmt.Errorf("the other computer has no screens")
	}
	if only >= len(local) {
		return nil, fmt.Errorf("there is no screen %d here (screens are numbered from 0, %d found)", only, len(local))
	}
	g.segs = exitSegments(g.local, only)
	if len(g.segs) == 0 {
		return nil, fmt.Errorf("no screen here has a free %s edge: another screen is past it everywhere", side)
	}
	return g, nil
}

// exitAt returns the segment the working-space point p leaves from, if p
// is on a free bottom edge.
func (g *geometry) exitAt(p image.Point) (segment, bool) {
	for _, s := range g.segs {
		if p.Y >= s.y && p.X >= s.x0 && p.X < s.x1 && g.onScreen(p.X, s.screen, p.Y) {
			return s, true
		}
	}
	return segment{}, false
}

// onScreen reports whether row y (or below, as a clamped pointer reports)
// is the bottom of local screen i at column x.
func (g *geometry) onScreen(x, i, y int) bool {
	m := g.local[i]
	return x >= m.Min.X && x < m.Max.X && y >= m.Max.Y-1
}

// entry is where the pointer appears on the remote screens when it leaves
// at working-space x: across the top of the remote layout, in proportion.
func (g *geometry) entry(x float64) fpoint {
	x0, x1 := span(g.segs)
	rb := box(g.remote)
	rx := scale(x+0.5, float64(x0), float64(x1), float64(rb.Min.X), float64(rb.Max.X)) - 0.5
	rx = min(max(rx, float64(rb.Min.X)), float64(rb.Max.X-1))
	// The topmost remote screen in that column, or the nearest one.
	best, bestD := -1, math.MaxFloat64
	for i, r := range g.remote {
		d := 0.0
		if rx < float64(r.Min.X) {
			d = float64(r.Min.X) - rx
		} else if rx >= float64(r.Max.X) {
			d = rx - float64(r.Max.X-1)
		}
		if d < bestD || d == bestD && r.Min.Y < g.remote[best].Min.Y {
			best, bestD = i, d
		}
	}
	r := g.remote[best]
	return fpoint{min(max(rx, float64(r.Min.X)), float64(r.Max.X-1)), float64(r.Min.Y)}
}

// back is where the pointer comes back onto the local screens when it
// leaves the remote ones upwards at working-space x: the matching place
// on the nearest exit segment, one row in from its edge.
func (g *geometry) back(x float64) image.Point {
	x0, x1 := span(g.segs)
	rb := box(g.remote)
	lx := scale(x+0.5, float64(rb.Min.X), float64(rb.Max.X), float64(x0), float64(x1)) - 0.5
	best, bestD := g.segs[0], math.MaxFloat64
	for _, s := range g.segs {
		d := 0.0
		if lx < float64(s.x0) {
			d = float64(s.x0) - lx
		} else if lx >= float64(s.x1) {
			d = lx - float64(s.x1-1)
		}
		if d < bestD {
			best, bestD = s, d
		}
	}
	px := int(math.Round(min(max(lx, float64(best.x0)), float64(best.x1-1))))
	return image.Pt(px, best.y-1)
}

// park is where the real pointer waits while the remote computer has it:
// a little in from the exit, so its movements are not cut off by the edge
// of the screen (they are measured from here).
func (g *geometry) park(s segment) func(x int) image.Point {
	m := g.local[s.screen]
	gap := min(parkGap, m.Dx()/2-1, m.Dy()/2-1)
	return func(x int) image.Point {
		x = min(max(x, m.Min.X+gap), m.Max.X-1-gap)
		return image.Pt(x, m.Max.Y-1-gap)
	}
}

// parkGap is how far from the screen's edges the real pointer is parked.
// A single mouse report rarely moves the pointer further than this.
const parkGap = 100

// remoteAt returns the remote screen holding p, or -1.
func (g *geometry) remoteAt(p fpoint) int {
	for i, r := range g.remote {
		if p.X >= float64(r.Min.X) && p.X < float64(r.Max.X) && p.Y >= float64(r.Min.Y) && p.Y < float64(r.Max.Y) {
			return i
		}
	}
	return -1
}

// topFree reports whether nothing of the remote layout is above remote
// screen i at column x, so moving up from it there goes back.
func (g *geometry) topFree(i int, x float64) bool {
	top := g.remote[i].Min.Y
	for j, r := range g.remote {
		if j != i && r.Max.Y <= top && x >= float64(r.Min.X) && x < float64(r.Max.X) {
			return false
		}
	}
	return true
}

// move moves the remote pointer from p (on remote screen cur) by d. It
// returns the new position and whether the pointer went off the top,
// back to this computer. Off any other edge it stops at the edge.
func (g *geometry) move(p fpoint, d fpoint) (fpoint, bool) {
	cur := g.remoteAt(p)
	if cur < 0 {
		p = g.entry(p.X)
		cur = g.remoteAt(p)
	}
	n := fpoint{p.X + d.X, p.Y + d.Y}
	if g.remoteAt(n) >= 0 {
		return n, false
	}
	r := g.remote[cur]
	if n.Y < float64(r.Min.Y) && g.topFree(cur, min(max(n.X, float64(r.Min.X)), float64(r.Max.X)-1)) {
		return n, true
	}
	// Slide along the edge: keep whichever part of the move stays on a
	// screen, else stop at this screen's edge.
	if c := (fpoint{n.X, clampF(n.Y, r.Min.Y, r.Max.Y)}); g.remoteAt(c) >= 0 {
		return c, false
	}
	if c := (fpoint{clampF(n.X, r.Min.X, r.Max.X), n.Y}); g.remoteAt(c) >= 0 {
		return c, false
	}
	return fpoint{clampF(n.X, r.Min.X, r.Max.X), clampF(n.Y, r.Min.Y, r.Max.Y)}, false
}

func clampF(v float64, lo, hi int) float64 {
	return min(max(v, float64(lo)), float64(hi)-0.001)
}

// normal turns a working-space remote position into 0..1 of the box
// around the remote screens, as the host's input expects.
func (g *geometry) normal(p fpoint) (float64, float64) {
	r := g.t.invF(p)
	b := g.rbox
	nx := (r.X - float64(b.Min.X)) / math.Max(1, float64(b.Dx()-1))
	ny := (r.Y - float64(b.Min.Y)) / math.Max(1, float64(b.Dy()-1))
	return min(max(nx, 0), 1), min(max(ny, 0), 1)
}

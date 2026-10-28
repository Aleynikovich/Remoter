//go:build windows && (amd64 || arm64)

package input

import (
	"image"
	"log"
	"time"
	"unsafe"
)

var (
	procMonitorFromPoint = user32.NewProc("MonitorFromPoint")
	procGetMonitorInfo   = user32.NewProc("GetMonitorInfoW")
)

// grid maps physical pixels on one monitor to SendInput's absolute
// 0..65535 coordinates. Windows does not always map that grid onto the
// virtual desktop the way its documentation says (Aley's two monitors put
// the pointer far to the right), so the mapping is measured: move to two
// points, see where the pointer went, fit a line per axis.
type grid struct {
	ok       bool
	retry    time.Time // when to measure again after a failure
	n0x, n0y float64   // grid position of the first measured point
	p0x, p0y float64   // where it landed
	kx, ky   float64   // grid units per pixel
	monitor  image.Rectangle
}

func (g *grid) at(p image.Point) (int32, int32) {
	return int32(g.n0x + (float64(p.X)-g.p0x)*g.kx + 0.5), int32(g.n0y + (float64(p.Y)-g.p0y)*g.ky + 0.5)
}

// monitorAt returns the monitor under p (or the nearest one) and its area.
func monitorAt(p image.Point) (uintptr, image.Rectangle) {
	const nearest = 2 // MONITOR_DEFAULTTONEAREST
	pt := uintptr(uint32(int32(p.X))) | uintptr(uint32(int32(p.Y)))<<32
	h, _, _ := procMonitorFromPoint.Call(pt, nearest)
	if h == 0 {
		return 0, image.Rectangle{}
	}
	var info struct {
		size    uint32
		monitor [4]int32
		work    [4]int32
		flags   uint32
	}
	info.size = uint32(unsafe.Sizeof(info))
	if ok, _, _ := procGetMonitorInfo.Call(h, uintptr(unsafe.Pointer(&info))); ok == 0 {
		return 0, image.Rectangle{}
	}
	m := info.monitor
	return h, image.Rect(int(m[0]), int(m[1]), int(m[2]), int(m[3]))
}

// documented is the absolute grid position of p as SendInput's
// documentation describes it, the starting guess for measuring.
func documented(p image.Point) (int32, int32, bool) {
	vx, vy := metric(smXVirtualScreen), metric(smYVirtualScreen)
	vw, vh := metric(smCXVirtualScreen), metric(smCYVirtualScreen)
	if vw <= 1 || vh <= 1 {
		return 0, 0, false
	}
	nx := int32((int64(p.X-vx)*65535 + int64(vw-1)/2) / int64(vw-1))
	ny := int32((int64(p.Y-vy)*65535 + int64(vh-1)/2) / int64(vh-1))
	return nx, ny, true
}

func sendAbsolute(nx, ny int32) {
	sendMouse(nx, ny, 0, mouseeventfMove|mouseeventfAbsolute|mouseeventfVirtualDsk)
}

// landed waits until the pointer is no longer at from and has settled, and
// returns where it is. Injected input is handled on another thread, so the
// move shows up a moment after SendInput returns.
func landed(from image.Point, wait time.Duration) (image.Point, bool) {
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
		p, ok := cursorPos()
		if !ok {
			return image.Point{}, false
		}
		if p != from {
			time.Sleep(3 * time.Millisecond)
			q, _ := cursorPos()
			return q, true
		}
	}
	return from, false
}

// measure fits g for the monitor at area m. It moves the pointer around on
// that monitor.
func (g *grid) measure(m image.Rectangle) bool {
	a := image.Pt(m.Min.X+m.Dx()/4, m.Min.Y+m.Dy()/4)
	b := image.Pt(m.Min.X+m.Dx()*3/4, m.Min.Y+m.Dy()*3/4)
	ax, ay, ok := documented(a)
	if !ok {
		return false
	}
	bx, by, _ := documented(b)
	// Twice: the first fit may come from points that landed on another
	// monitor, the second uses points that land on this one.
	for round := 0; round < 2; round++ {
		center := image.Pt(m.Min.X+m.Dx()/2, m.Min.Y+m.Dy()/2)
		if ok, _, _ := procSetCursorPos.Call(uintptr(center.X), uintptr(center.Y)); ok == 0 {
			return false
		}
		sendAbsolute(ax, ay)
		ga, ok := landed(center, 100*time.Millisecond)
		if !ok {
			return false
		}
		sendAbsolute(bx, by)
		gb, ok := landed(ga, 100*time.Millisecond)
		if !ok || gb.X == ga.X || gb.Y == ga.Y {
			return false
		}
		g.n0x, g.n0y = float64(ax), float64(ay)
		g.p0x, g.p0y = float64(ga.X), float64(ga.Y)
		g.kx = float64(bx-ax) / float64(gb.X-ga.X)
		g.ky = float64(by-ay) / float64(gb.Y-ga.Y)
		if g.kx <= 0 || g.ky <= 0 {
			return false
		}
		ax, ay = g.at(a)
		bx, by = g.at(b)
	}
	// Check the fit on a third point.
	c := image.Pt(m.Min.X+m.Dx()/3, m.Min.Y+m.Dy()*2/3)
	from, _ := cursorPos()
	sendAbsolute(g.at(c))
	got, ok := landed(from, 100*time.Millisecond)
	if !ok || abs(got.X-c.X) > 1 || abs(got.Y-c.Y) > 1 {
		log.Printf("input: absolute mouse grid on monitor %v does not fit (asked %v, got %v)", m, c, got)
		return false
	}
	nx, ny, _ := documented(c)
	cx, cy := g.at(c)
	log.Printf("input: absolute mouse grid on monitor %v measured; documented grid is off by %d,%d units", m, nx-cx, ny-cy)
	g.ok = true
	g.monitor = m
	return true
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// pointer moves the Windows pointer with real (absolute SendInput) mouse
// events, which games and hover effects react to, on a measured grid so
// it lands on the exact pixel. Where measuring fails (the secure desktop,
// for one), it places the pointer with SetCursorPos instead.
type pointer struct {
	grids map[uintptr]*grid
}

func (pt *pointer) move(p image.Point, exact bool) {
	h, m := monitorAt(p)
	if pt.grids == nil {
		pt.grids = make(map[uintptr]*grid)
	}
	g := pt.grids[h]
	if g == nil || (g.ok && g.monitor != m) {
		g = &grid{}
		pt.grids[h] = g
	}
	if !g.ok && h != 0 && time.Now().After(g.retry) && !g.measure(m) {
		g.retry = time.Now().Add(2 * time.Minute)
	}
	if !g.ok {
		place(p)
		return
	}
	from, _ := cursorPos()
	sendAbsolute(g.at(p))
	if !exact || from == p {
		return
	}
	// Before a click, make sure it is there.
	if got, _ := landed(from, 30*time.Millisecond); got != p {
		place(p)
		if abs(got.X-p.X) > 1 || abs(got.Y-p.Y) > 1 {
			log.Printf("input: pointer went to %v instead of %v; measuring the mouse grid again", got, p)
			g.ok = false
		}
	}
}

// place puts the pointer on p with SetCursorPos, then sends a move of
// nothing so programs reading raw input notice; SendInput's absolute grid
// is the last resort.
func place(p image.Point) {
	if ok, _, _ := procSetCursorPos.Call(uintptr(p.X), uintptr(p.Y)); ok != 0 {
		sendMouse(0, 0, 0, mouseeventfMove)
		return
	}
	if nx, ny, ok := documented(p); ok {
		sendAbsolute(nx, ny)
	}
}

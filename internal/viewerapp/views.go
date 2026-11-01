//go:build linux || windows

package viewerapp

import (
	"image"
	"log"
	"time"

	"github.com/aleynikovich/remoter/internal/protocol"
	"github.com/aleynikovich/remoter/internal/viewer"
)

// Virtual monitors: the host can stream only part of a display, so a wide
// monitor can be watched (and controlled) as several pieces shaped like this
// screen, or as areas the user draws. Zoom is the viewer-only alternative:
// the whole picture comes, and a piece of it that fills the window follows
// the pointer.

const scZ = 29

// viewAspect is the shape of the screen the window is on, which "fit"
// virtual monitors take.
func (a *app) viewAspect() float64 {
	var r sdlRect
	if sdl.GetDisplayBounds(sdl.GetWindowDisplayIndex(a.win), &r) == 0 && r.W > 0 && r.H > 0 {
		return float64(r.W) / float64(r.H)
	}
	if a.out.X > 0 && a.out.Y > 0 {
		return float64(a.out.X) / float64(a.out.Y)
	}
	return 16.0 / 10
}

func (a *app) split() string {
	if a.cfg.Split == "" {
		return "fit"
	}
	return a.cfg.Split
}

// views lists what the host can show, virtual monitors included.
func (a *app) views() []viewer.View {
	if a.displays < 1 {
		return nil
	}
	return viewer.Views(a.displays, a.allShown, a.sizes, a.viewAspect(), a.split(), a.cfg.Areas, a.server)
}

// currentView is the index in views of what the host shows, or -1.
func (a *app) currentView(vs []viewer.View) int {
	for i, v := range vs {
		if v.Display == a.display && viewer.SameRegion(v.Region, a.region) {
			return i
		}
	}
	return -1
}

// viewLabel names what the host shows now.
func (a *app) viewLabel() string {
	vs := a.views()
	if i := a.currentView(vs); i >= 0 {
		return vs[i].Label
	}
	if a.region != nil {
		return "Part of " + a.monitorLabel(a.display)
	}
	return a.monitorLabel(a.display)
}

// selectView asks the host to show a view.
func (a *app) selectView(v viewer.View) {
	if !a.connected {
		return
	}
	a.releaseHeld()
	a.display, a.region = v.Display, v.Region
	a.client.Send(protocol.Message{Type: protocol.TypeSelectDisplay, Display: v.Display, Region: v.Region})
	a.showNotice(v.Label, 2*time.Second)
	a.dirty = true
}

// stepView shows the next (step 1) or previous (-1) view.
func (a *app) stepView(step int) {
	vs := a.views()
	if len(vs) < 2 {
		return
	}
	i := a.currentView(vs)
	if i < 0 {
		i = 0
	}
	a.selectView(vs[(i+step+len(vs))%len(vs)])
}

func (a *app) saveConfig() {
	if a.cfgPath == "" {
		return
	}
	if err := a.cfg.Save(a.cfgPath); err != nil {
		log.Printf("saving settings: %v", err)
	}
}

// nextSplit switches to the next way of cutting displays.
func (a *app) nextSplit() {
	cur := a.split()
	next := viewer.Splits[0]
	for i, s := range viewer.Splits {
		if s == cur {
			next = viewer.Splits[(i+1)%len(viewer.Splits)]
		}
	}
	a.cfg.Split = next
	if next == "fit" {
		a.cfg.Split = ""
	}
	a.saveConfig()
}

func (a *app) setZoom(on bool) {
	a.cfg.Zoom = on
	a.saveConfig()
	if on {
		a.showNotice("Zoom: the view fills the window and follows the mouse  ·  Ctrl+Alt+Z shows the whole picture", 4*time.Second)
	} else {
		a.showNotice("Whole picture", 2*time.Second)
	}
	a.dirty = true
}

// startDrawing lets the user drag out an area of the picture to make a
// virtual monitor of.
func (a *app) startDrawing() {
	if !a.connected {
		return
	}
	if a.captured {
		a.releaseHeld()
		a.capture(false)
	}
	a.drawing, a.dragging = true, false
	a.showNotice("Drag across the picture to make a virtual monitor  ·  Esc cancels", time.Minute)
	a.dirty = true
}

func (a *app) cancelDrawing() {
	a.drawing, a.dragging = false, false
	a.showNotice("", 0)
	a.dirty = true
}

// drawButton handles a mouse button while drawing an area.
func (a *app) drawButton(down bool) {
	x, y := a.remotePoint()
	if down {
		a.dragging = true
		a.dragFrom, a.dragTo = [2]float64{x, y}, [2]float64{x, y}
		return
	}
	if !a.dragging {
		return
	}
	a.dragTo = [2]float64{x, y}
	a.drawing, a.dragging = false, false
	a.showNotice("", 0)
	r := protocol.Region{
		X: min(a.dragFrom[0], a.dragTo[0]), Y: min(a.dragFrom[1], a.dragTo[1]),
		W: abs(a.dragTo[0] - a.dragFrom[0]), H: abs(a.dragTo[1] - a.dragFrom[1]),
	}
	if r.W < 0.03 || r.H < 0.03 {
		a.showNotice("That area is too small; drag across more of the picture", 3*time.Second)
		return
	}
	// The picture may be a virtual monitor already.
	r = viewer.Within(a.region, r)
	if a.display >= len(a.sizes) {
		a.showNotice("The remote computer is too old to show areas; update it", 4*time.Second)
		return
	}
	s := a.sizes[a.display]
	a.cfg.Areas = append(a.cfg.Areas, viewer.Area{Server: a.server, Display: a.display, W: s.W, H: s.H, Region: r})
	a.saveConfig()
	vs := a.views()
	for i := len(vs) - 1; i >= 0; i-- {
		if vs[i].Custom && vs[i].Display == a.display && viewer.SameRegion(vs[i].Region, &r) {
			a.selectView(vs[i])
			return
		}
	}
}

// forgetAreas removes the areas drawn for this computer.
func (a *app) forgetAreas() {
	var keep []viewer.Area
	for _, ar := range a.cfg.Areas {
		if ar.Server != a.server {
			keep = append(keep, ar)
		}
	}
	a.cfg.Areas = keep
	a.saveConfig()
	if a.region != nil && a.currentView(a.views()) < 0 {
		a.selectView(viewer.View{Display: a.display, Label: a.monitorLabel(a.display)})
	}
}

func (a *app) hasAreas() bool {
	for _, ar := range a.cfg.Areas {
		if ar.Server == a.server {
			return true
		}
	}
	return false
}

func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

// placePicture works out which part of a size picture to draw (src, in
// picture pixels) and where (a.dst), zoomed when that is on.
func (a *app) placePicture(size, out image.Point) (src *sdlRect) {
	a.zoomed = false
	if a.cfg.Zoom && !a.drawing && out.X > 0 && out.Y > 0 {
		fx := a.mouse[0] * a.scale / float64(out.X)
		fy := a.mouse[1] * a.scale / float64(out.Y)
		x0, y0, x1, y1, ok := viewer.Zoom(size.X, size.Y, out.X, out.Y, fx, fy)
		if ok {
			a.zoomed = true
			a.zoomSrc = [4]float64{x0, y0, x1, y1}
			a.picSize = size
			a.dst = viewer.Fit(int(x1-x0+0.5), int(y1-y0+0.5), out)
			return &sdlRect{int32(x0 + 0.5), int32(y0 + 0.5), int32(x1 - x0 + 0.5), int32(y1 - y0 + 0.5)}
		}
	}
	a.dst = viewer.Fit(size.X, size.Y, out)
	return nil
}

// drawSelection outlines the area being dragged out.
func (a *app) drawSelection() {
	if !a.drawing || !a.dragging || a.dst.Empty() {
		return
	}
	px := func(x, y float64) (int32, int32) {
		return int32(float64(a.dst.Min.X) + x*float64(a.dst.Dx())), int32(float64(a.dst.Min.Y) + y*float64(a.dst.Dy()))
	}
	x0, y0 := px(min(a.dragFrom[0], a.dragTo[0]), min(a.dragFrom[1], a.dragTo[1]))
	x1, y1 := px(max(a.dragFrom[0], a.dragTo[0]), max(a.dragFrom[1], a.dragTo[1]))
	sdl.SetRenderDrawBlendMode(a.ren, sdlBlendBlend)
	sdl.SetRenderDrawColor(a.ren, colFocus.R, colFocus.G, colFocus.B, 0x40)
	sdl.RenderFillRect(a.ren, &sdlRect{x0, y0, x1 - x0, y1 - y0})
	sdl.SetRenderDrawColor(a.ren, colFocus.R, colFocus.G, colFocus.B, 0xff)
	t := int32(max(2*a.scale, 1))
	for _, r := range []sdlRect{{x0, y0, x1 - x0, t}, {x0, y1 - t, x1 - x0, t}, {x0, y0, t, y1 - y0}, {x1 - t, y0, t, y1 - y0}} {
		sdl.RenderFillRect(a.ren, &r)
	}
}

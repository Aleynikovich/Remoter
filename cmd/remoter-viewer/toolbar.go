//go:build linux || windows

package main

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aleynikovich/remoter/internal/protocol"
	"github.com/aleynikovich/remoter/internal/share"
	"github.com/aleynikovich/remoter/internal/viewer"
)

// The toolbar slides down from the top edge of a session: point at the tab
// in the middle of the top edge, or press Ctrl+Alt+M.

var (
	colBar      = color.RGBA{0x20, 0x23, 0x2b, 0xf0}
	colBarHover = color.RGBA{0x34, 0x39, 0x46, 0xff}
	colTab      = color.RGBA{0x4c, 0x8d, 0xff, 0xb0}
)

type toolbar struct {
	open     bool
	sticky   bool // opened with the hotkey: stays until closed
	keys     bool // the Send keys row is showing
	picture  bool // the picture settings row is showing
	hover    int  // item under the pointer, or -1
	tabSince time.Time

	items []tbItem
	hit   []image.Rectangle // items as drawn, in output pixels
	area  image.Rectangle   // the whole bar, in output pixels
	tex   uintptr
	key   string // what tex shows
}

type tbItem struct {
	id, label string
}

// sendKeys are the combinations the Send keys row offers, as
// KeyboardEvent.code values pressed in order and released in reverse.
var sendKeys = []struct {
	label string
	codes []string
}{
	{"Windows key", []string{"MetaLeft"}},
	{"Alt+Tab", []string{"AltLeft", "Tab"}},
	{"Ctrl+Shift+Esc", []string{"ControlLeft", "ShiftLeft", "Escape"}},
	{"Win+L", []string{"MetaLeft", "KeyL"}},
	{"Print Screen", []string{"PrintScreen"}},
	{"Alt+F4", []string{"AltLeft", "F4"}},
}

func (a *app) toolbarItems() []tbItem {
	t := &a.bar
	if t.picture {
		items := []tbItem{{"picture", "‹ Back"}}
		cur := a.cfg.Quality
		if cur == "" {
			cur = "auto"
		}
		for _, q := range qualities {
			label := qualityLabel(q)
			if q == cur {
				label = "• " + label
			}
			items = append(items, tbItem{"q:" + q, label})
		}
		return items
	}
	if t.keys {
		items := []tbItem{{"keys", "‹ Back"}}
		for i, k := range sendKeys {
			items = append(items, tbItem{fmt.Sprintf("key%d", i), k.label})
		}
		return items
	}
	q := a.cfg.Quality
	if q == "" {
		q = "auto"
	}
	picture := "Picture: " + qualityText(q, a.qualityScale)
	items := []tbItem{{"picture", picture + "…"}}
	if a.displays > 1 {
		items = append(items, tbItem{"monitor", fmt.Sprintf("Monitor %d/%d", a.display+1, a.displays)})
	}
	full := "Full screen"
	if a.fullscreen() {
		full = "Leave full screen"
	}
	capture := "Release mouse"
	if !a.captured {
		capture = "Take control"
	}
	return append(items,
		tbItem{"fullscreen", full},
		tbItem{"keys", "Send keys…"},
		tbItem{"capture", capture},
		tbItem{"send", "Send files…"},
		tbItem{"linktest", "Link test"},
		tbItem{"diag", "Copy diagnostics"},
		tbItem{"devices", "Devices"},
		tbItem{"close", "×"},
	)
}

// tabRect is the hover target along the top edge, in output pixels.
func (a *app) tabRect() image.Rectangle {
	w := int(120 * a.scale)
	return image.Rect((a.out.X-w)/2, 0, (a.out.X+w)/2, int(6*a.scale))
}

func (a *app) openToolbar(sticky bool) {
	a.bar.open, a.bar.sticky, a.bar.keys, a.bar.picture, a.bar.hover = true, sticky, false, false, -1
	a.dirty = true
}

func (a *app) closeToolbar() {
	a.bar.open, a.bar.keys, a.bar.picture, a.bar.tabSince = false, false, false, time.Time{}
	a.dirty = true
}

// toolbarMotion follows the pointer. It reports whether the toolbar has the
// pointer, so the move is not sent to the remote computer.
func (a *app) toolbarMotion() bool {
	if !a.connected {
		return false
	}
	t := &a.bar
	p := image.Pt(int(a.mouse[0]*a.scale), int(a.mouse[1]*a.scale))
	if !t.open {
		if p.In(a.tabRect()) {
			if t.tabSince.IsZero() {
				t.tabSince = time.Now()
			}
		} else {
			t.tabSince = time.Time{}
		}
		return false
	}
	hover := -1
	for i, r := range t.hit {
		if p.In(r) {
			hover = i
		}
	}
	if hover != t.hover {
		t.hover = hover
		a.dirty = true
	}
	if p.In(t.area) {
		return true
	}
	// Opened by pointing: it goes away once the pointer moves well below.
	if !t.sticky && p.Y > t.area.Max.Y+int(60*a.scale) {
		a.closeToolbar()
	}
	return false
}

// toolbarClick handles a click. It reports whether the toolbar took it.
func (a *app) toolbarClick(down bool) bool {
	t := &a.bar
	if !t.open {
		return false
	}
	p := image.Pt(int(a.mouse[0]*a.scale), int(a.mouse[1]*a.scale))
	if !p.In(t.area) {
		if down && t.sticky {
			a.closeToolbar()
			return true
		}
		return false
	}
	if !down {
		return true
	}
	for i, r := range t.hit {
		if p.In(r) && i < len(t.items) {
			a.toolbarAction(t.items[i].id)
			break
		}
	}
	return true
}

func (a *app) toolbarAction(id string) {
	t := &a.bar
	switch id {
	case "picture":
		t.picture = !t.picture
	case "monitor":
		a.shortcut(scPageDown)
	case "fullscreen":
		a.closeToolbar()
		a.shortcut(scEnter)
	case "keys":
		t.keys = !t.keys
	case "capture":
		a.closeToolbar()
		if a.captured {
			a.shortcut(scD)
		} else {
			a.capture(true)
		}
	case "send":
		a.closeToolbar()
		a.chooseFiles()
	case "linktest":
		a.closeToolbar()
		a.runLinkTest()
	case "diag":
		a.closeToolbar()
		a.shortcut(scI)
	case "devices":
		a.closeToolbar()
		a.disconnect("")
	case "close":
		a.closeToolbar()
	default:
		if q, ok := strings.CutPrefix(id, "q:"); ok {
			a.closeToolbar()
			a.setQuality(q)
			break
		}
		var i int
		if _, err := fmt.Sscanf(id, "key%d", &i); err == nil && i < len(sendKeys) {
			a.closeToolbar()
			a.sendCombo(sendKeys[i].codes)
			a.showNotice("Sent "+sendKeys[i].label, 2*time.Second)
		}
	}
	a.dirty = true
}

// sendCombo presses the keys in order and lets go in reverse.
func (a *app) sendCombo(codes []string) {
	if !a.connected {
		return
	}
	for _, c := range codes {
		a.client.Input(protocol.Input{Kind: "keydown", Code: c})
	}
	for i := len(codes) - 1; i >= 0; i-- {
		a.client.Input(protocol.Input{Kind: "keyup", Code: codes[i]})
	}
}

// drawToolbar draws the tab or the open bar over the session.
func (a *app) drawToolbar(out image.Point) {
	t := &a.bar
	if !a.connected {
		return
	}
	if !t.open {
		r := a.tabRect()
		col := colTab
		if !t.tabSince.IsZero() {
			col = colFocus
		}
		sdl.SetRenderDrawBlendMode(a.ren, sdlBlendBlend)
		sdl.SetRenderDrawColor(a.ren, col.R, col.G, col.B, col.A)
		d := sdlRect{int32(r.Min.X), int32(r.Min.Y), int32(r.Dx()), int32(int(3 * a.scale))}
		sdl.RenderFillRect(a.ren, &d)
		return
	}
	t.items = a.toolbarItems()
	key := fmt.Sprint(t.items, t.hover, a.scale, out.X)
	if key != t.key || t.tex == 0 {
		if t.tex != 0 {
			sdl.DestroyTexture(t.tex)
		}
		img, hit := toolbarImage(t.items, t.hover, a.scale, out.X)
		t.tex, t.key = a.texture(img), key
		x := (out.X - img.Rect.Dx()) / 2
		t.area = img.Rect.Add(image.Pt(x, 0))
		t.hit = t.hit[:0]
		for _, r := range hit {
			t.hit = append(t.hit, r.Add(image.Pt(x, 0)))
		}
	}
	d := sdlRect{int32(t.area.Min.X), int32(t.area.Min.Y), int32(t.area.Dx()), int32(t.area.Dy())}
	sdl.RenderCopy(a.ren, t.tex, nil, &d)
}

// toolbarImage lays the items out in a row (two if the window is narrow)
// and returns where each one went.
func toolbarImage(items []tbItem, hover int, s float64, maxW int) (*image.RGBA, []image.Rectangle) {
	px := func(v float64) int { return int(v*s + 0.5) }
	f := face(px(14))
	pad, gap, h := px(12), px(4), px(32)
	var hit []image.Rectangle
	x, y, w := px(8), px(6), 0
	for _, it := range items {
		iw := textWidth(f, it.label) + 2*pad
		if x+iw+px(8) > maxW-px(16) && x > px(8) {
			x, y = px(8), y+h+gap
		}
		hit = append(hit, image.Rect(x, y, x+iw, y+h))
		x += iw + gap
		w = max(w, x-gap+px(8))
	}
	r := image.Rect(0, 0, w, y+h+px(6))
	img := image.NewRGBA(r)
	fill(img, r, colBar)
	for i, it := range items {
		b := hit[i]
		if i == hover {
			fill(img, b, colBarHover)
		}
		c := colText
		if it.id == "close" {
			c = colMuted
		}
		drawText(img, f, b.Min.X+pad, b.Min.Y+px(21), it.label, c)
	}
	fill(img, image.Rect(0, r.Max.Y-px(2), r.Max.X, r.Max.Y), colFocus)
	return img, hit
}

// runLinkTest measures the connection and shows the result.
func (a *app) runLinkTest() {
	if !a.connected {
		return
	}
	a.showNotice("Testing the connection…", 15*time.Second)
	base := viewer.HTTPBase(a.client.URL)
	go func() {
		r := linkTest(base)
		a.later(func() { a.showNotice(r, 10*time.Second) })
	}()
}

// sendFiles sends files to the computer of the session, which saves them
// in its Downloads folder.
func (a *app) sendFiles(paths []string) {
	if !a.connected || len(paths) == 0 {
		return
	}
	base := viewer.HTTPBase(a.client.URL)
	server := a.server
	a.showNotice(fmt.Sprintf("Sending %d file(s) to %s…", len(paths), server), 30*time.Second)
	go func() {
		host, _ := os.Hostname()
		c := &share.Client{From: host}
		for i, p := range paths {
			name := filepath.Base(p)
			last := -1
			err := c.SendFile(context.Background(), base, p, i+1, len(paths), func(sent, total int64) {
				if pct := int(sent * 100 / max(total, 1)); pct != last {
					last = pct
					a.later(func() {
						a.showNotice(fmt.Sprintf("Sending %s to %s: %d%%", name, server, pct), 30*time.Second)
					})
				}
			})
			if err != nil {
				a.later(func() { a.showNotice("Could not send "+name+": "+err.Error(), 10*time.Second) })
				return
			}
		}
		a.later(func() {
			a.showNotice(fmt.Sprintf("Sent %d file(s) to %s (saved in its Downloads)", len(paths), server), 6*time.Second)
		})
	}()
}

// chooseFiles asks for files to send with the system's file chooser.
func (a *app) chooseFiles() {
	if a.captured {
		a.releaseHeld()
		a.capture(false)
	}
	go func() {
		paths, err := pickFiles()
		a.later(func() {
			if err != nil {
				a.showNotice(err.Error()+"  ·  you can also drop files on the window", 8*time.Second)
				return
			}
			a.sendFiles(paths)
		})
	}()
}

// later runs fn on the window's thread. Only other goroutines call it.
func (a *app) later(fn func()) {
	a.jobs <- fn
	a.wakeUp()
}

func (a *app) drainJobs() {
	for {
		select {
		case fn := <-a.jobs:
			fn()
		default:
			return
		}
	}
}

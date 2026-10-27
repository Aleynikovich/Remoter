//go:build linux

package main

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"
	"log"
	"os"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
	"unsafe"

	"github.com/aleynikovich/remoter/internal/protocol"
	"github.com/aleynikovich/remoter/internal/stream"
	"github.com/aleynikovich/remoter/internal/viewer"
)

const (
	modeNone = iota
	modeTiles
	modeVideo
)

// scancodes the viewer itself looks at.
const (
	scEnter    = 40
	scEscape   = 41
	scBack     = 42
	scTab      = 43
	scPageUp   = 75
	scPageDown = 78
	scKPEnter  = 88
	scV        = 25
)

// SDL's system cursors for the CSS cursor names the host sends.
var systemCursors = map[string]int32{
	"default": 0, "text": 1, "wait": 2, "crosshair": 3, "progress": 4,
	"nwse-resize": 5, "nesw-resize": 6, "ew-resize": 7, "ns-resize": 8,
	"move": 9, "not-allowed": 10, "pointer": 11, "help": 0,
}

type app struct {
	win, ren uintptr
	wakeType uint32
	woken    atomic.Bool
	quit     bool
	update   *atomic.Bool // a newer version is installed and waits for a restart

	cfg     viewer.Config
	cfgPath string
	form    *form
	formTex uintptr

	client    *viewer.Client
	cancel    context.CancelFunc
	connected bool
	server    string

	dec       *decoder
	decErr    error
	lastIDR   time.Time
	needKey   bool // frames before the first key frame cannot be decoded
	mode      int
	videoTex  uintptr
	videoSize image.Point
	tileTex   uintptr
	tileSize  image.Point
	acks      []uint32

	captured bool
	held     map[string]string // key codes down on the remote, and their key
	buttons  map[int]bool
	swallow  map[int32]bool // scancodes whose key-up belongs to a viewer shortcut
	mouse    [2]float64     // last pointer position, window units
	moved    bool
	dst      image.Rectangle // where the remote screen is drawn, output pixels
	scale    float64         // output pixels per window unit

	cursors  map[string]uintptr
	clip     string // clipboard text both sides already have
	displays int
	display  int

	notice      string
	noticeUntil time.Time
	noticeTex   uintptr
	noticeSize  image.Point

	frames, bytes int
	statsAt       time.Time
	title         string
	dirty         bool
}

func newApp(cfg viewer.Config, cfgPath string, fullscreen bool, avErr error, update *atomic.Bool) (*app, error) {
	sdl.SetHint("SDL_VIDEO_WAYLAND_WMCLASS", "remoter-viewer")
	sdl.SetHint("SDL_VIDEO_X11_WMCLASS", "remoter-viewer")
	sdl.SetHint("SDL_APP_NAME", "Remoter")
	sdl.SetHint("SDL_MOUSE_FOCUS_CLICKTHROUGH", "1")
	sdl.SetHint("SDL_RENDER_SCALE_QUALITY", "linear")
	// The remote screen is drawn on every frame; keeping the compositor
	// from waiting on vblank saves up to a frame of delay.
	sdl.SetHint("SDL_RENDER_VSYNC", "0")
	sdl.SetHint("SDL_VIDEO_ALLOW_SCREENSAVER", "1")
	// Native Wayland, not XWayland: it is what lets Hyprland, Sway, GNOME
	// and KDE hand the viewer their shortcuts (keyboard-shortcuts-inhibit)
	// and keep the pointer in the window (pointer-constraints). SDL 2 still
	// picks X11 first on its own.
	if os.Getenv("WAYLAND_DISPLAY") != "" && os.Getenv("SDL_VIDEODRIVER") == "" {
		sdl.SetHint("SDL_VIDEODRIVER", "wayland")
		if sdl.Init(sdlInitVideo) != 0 {
			log.Printf("Wayland: %s; trying X11", sdl.GetError())
			sdl.SetHint("SDL_VIDEODRIVER", "")
		}
	}
	if sdl.GetCurrentVideoDriver() == "" && sdl.Init(sdlInitVideo) != 0 {
		return nil, fmt.Errorf("start SDL: %s", sdl.GetError())
	}
	log.Printf("display: %s", sdl.GetCurrentVideoDriver())
	flags := uint32(sdlWindowResizable | sdlWindowAllowHighDPI)
	if fullscreen {
		flags |= sdlWindowFullscreenDesktop
	}
	win := sdl.CreateWindow("Remoter", sdlWindowPosCentered, sdlWindowPosCentered, 1280, 800, flags)
	if win == 0 {
		return nil, fmt.Errorf("open window: %s", sdl.GetError())
	}
	sdl.SetWindowMinimumSize(win, 480, 360)
	ren := sdl.CreateRenderer(win, -1, sdlRendererAccelerated)
	if ren == 0 {
		return nil, fmt.Errorf("start renderer: %s", sdl.GetError())
	}
	// The host's encoder marks its video BT.601; SDL would otherwise pick
	// BT.709 for HD sizes and shift the colours.
	sdl.SetYUVConversionMode(sdlYUVBT601)
	sdl.SetRenderDrawBlendMode(ren, sdlBlendBlend)

	a := &app{
		win: win, ren: ren,
		wakeType: sdl.RegisterEvents(1),
		update:   update,
		cfg:      cfg, cfgPath: cfgPath,
		form:    newForm(cfg.Server, cfg.ID, cfg.Password),
		decErr:  avErr,
		held:    map[string]string{},
		buttons: map[int]bool{},
		swallow: map[int32]bool{},
		cursors: map[string]uintptr{},
		scale:   1,
		dirty:   true,
	}
	if avErr != nil {
		log.Printf("H.264 off, using JPEG tiles: %v", avErr)
	}
	sdl.StartTextInput()
	return a, nil
}

// wakeUp interrupts the event wait; the client calls it for every event.
func (a *app) wakeUp() {
	if a.woken.Swap(true) {
		return
	}
	var ev sdlEvent
	*(*uint32)(unsafe.Pointer(&ev[0])) = a.wakeType
	sdl.PushEvent(&ev)
}

// run shows the window until it is closed. It reports whether the viewer
// should restart into an update.
func (a *app) run() bool {
	var ev sdlEvent
	for !a.quit {
		if sdl.WaitEventTimeout(&ev, 250) != 0 {
			a.handle(&ev)
			for sdl.PollEvent(&ev) != 0 {
				a.handle(&ev)
			}
		}
		a.woken.Store(false)
		a.drainClient()
		a.flushMove()
		a.tick()
		if a.dirty {
			a.render()
		}
		if a.client != nil {
			for _, seq := range a.acks {
				a.client.Ack(seq)
			}
		}
		a.acks = a.acks[:0]
		if a.client == nil && a.update.Load() {
			sdl.Quit()
			return true
		}
	}
	a.disconnect("")
	sdl.Quit()
	return false
}

func (a *app) handle(ev *sdlEvent) {
	switch ev.typ() {
	case sdlQuit:
		a.quit = true
	case sdlWindowEvent:
		switch ev.u8(12) {
		case sdlWindowEventFocusLost:
			a.releaseHeld()
		case sdlWindowEventFocusGained:
			a.syncClipboard()
		}
		a.dirty = true
	case sdlRenderReset, sdlRenderReset + 1:
		// Textures may be gone with the GPU context; start from a full frame.
		a.videoTex, a.tileTex, a.formTex, a.noticeTex = 0, 0, 0, 0
		a.videoSize, a.tileSize = image.Point{}, image.Point{}
		a.mode = modeNone
		if a.connected {
			a.client.Refresh(a.dec != nil)
		}
		a.dirty = true
	case sdlKeyDown, sdlKeyUp:
		if a.client != nil {
			a.sessionKey(ev, ev.typ() == sdlKeyDown)
		} else if ev.typ() == sdlKeyDown {
			a.formKey(ev)
		}
	case sdlTextInput:
		if a.client == nil && a.form.focus < 3 {
			text := ev[12:44]
			if i := bytes.IndexByte(text, 0); i >= 0 {
				text = text[:i]
			}
			a.form.values[a.form.focus] += string(text)
			a.formChanged()
		}
	case sdlMouseMotion:
		a.mouse = [2]float64{float64(ev.i32(20)), float64(ev.i32(24))}
		a.moved = a.connected
	case sdlMouseButtonDown, sdlMouseButtonUp:
		a.mouse = [2]float64{float64(ev.i32(20)), float64(ev.i32(24))}
		a.mouseButton(int(ev.u8(16)), ev.typ() == sdlMouseButtonDown)
	case sdlMouseWheel:
		if !a.connected {
			return
		}
		y := float64(ev.f32(32)) // precise, for touchpads; SDL 2.0.18+
		if y == 0 {
			y = float64(ev.i32(20))
		}
		if ev.u32(24) == sdlMouseWheelFlipped {
			y = -y
		}
		// The host expects browser units: about 100 per notch, down positive.
		a.flushMove()
		x, yy := a.remotePoint()
		a.client.Input(protocol.Input{Kind: "wheel", X: x, Y: yy, DeltaY: -y * 100})
	case sdlClipboardUpdate:
		a.syncClipboard()
	}
}

// ---- connect screen ----

func (a *app) formChanged() {
	a.form.status, a.form.err = "", false
	a.dirty = true
}

func (a *app) formKey(ev *sdlEvent) {
	sc, mod := ev.i32(16), ev.u16(24)
	f := a.form
	switch {
	case sc == scTab && mod&kmodShift != 0:
		f.focus = (f.focus + 3) % 4
	case sc == scTab:
		f.focus = (f.focus + 1) % 4
	case sc == scEnter || sc == scKPEnter:
		a.connect()
	case sc == scBack && f.focus < 3:
		v := f.values[f.focus]
		_, size := utf8.DecodeLastRuneInString(v)
		f.values[f.focus] = v[:len(v)-size]
	case sc == scV && mod&kmodCtrl != 0 && f.focus < 3:
		f.values[f.focus] += clipboardText()
	default:
		return
	}
	a.formChanged()
}

func (a *app) formClick(x, y int) {
	p := image.Pt(int(float64(x)*a.scale), int(float64(y)*a.scale))
	for i, r := range a.form.hit {
		if p.In(r) {
			a.form.focus = i
			a.formChanged()
			if i == 3 {
				a.connect()
			}
			return
		}
	}
}

func (a *app) connect() {
	f := a.form
	c, err := viewer.NewClient(f.values[0], f.values[1], f.values[2])
	if err != nil {
		f.status, f.err = err.Error(), true
		a.dirty = true
		return
	}
	a.cfg = viewer.Config{Server: f.values[0], ID: f.values[1], Password: f.values[2]}
	if a.cfgPath != "" {
		if err := a.cfg.Save(a.cfgPath); err != nil {
			log.Printf("save settings: %v", err)
		}
	}
	if a.dec == nil && a.decErr == nil {
		a.dec, a.decErr = newDecoder()
		if a.decErr != nil {
			log.Printf("H.264 off, using JPEG tiles: %v", a.decErr)
		}
	}
	c.Wake = a.wakeUp
	ctx, cancel := context.WithCancel(context.Background())
	a.client, a.cancel, a.server = c, cancel, f.values[0]
	go c.Run(ctx)
	sdl.StopTextInput()
	a.showNotice("Connecting to "+a.server+"…", time.Hour)
	a.setTitle("Remoter · connecting to " + a.server)
}

// disconnect closes the session and goes back to the connect screen with
// status shown on it.
func (a *app) disconnect(status string) {
	if a.client == nil {
		return
	}
	a.releaseHeld()
	a.cancel()
	a.client, a.cancel, a.connected = nil, nil, false
	a.capture(false)
	a.setCursor("default")
	a.mode = modeNone
	a.acks = a.acks[:0]
	a.notice = ""
	a.form.status, a.form.err = status, status != "" && status != "Disconnected."
	a.setTitle("Remoter")
	sdl.StartTextInput()
	a.dirty = true
}

// ---- session ----

func (a *app) drainClient() {
	if a.client == nil {
		return
	}
	pending := false // a decoded video picture waits to be shown
	for a.client != nil {
		var ev viewer.Event
		select {
		case ev = <-a.client.Events():
		default:
			if pending {
				a.showPicture()
			}
			return
		}
		switch ev.Kind {
		case viewer.EventConnected:
			a.connected = true
			a.releaseHeld()
			if a.dec != nil {
				a.dec.reset()
				a.needKey = true
			}
			a.client.Refresh(a.dec != nil)
			a.capture(true)
			a.syncClipboard()
			a.setTitle("Remoter · " + a.server)
			a.showNotice("Keyboard and mouse go to the remote computer  ·  Ctrl+Alt+Esc releases them", 5*time.Second)
		case viewer.EventFrame:
			a.bytes += len(ev.Data)
			if len(ev.Data) > 0 && ev.Data[0] == 'V' {
				pending = a.videoFrame(ev.Data) || pending
			} else {
				if pending {
					a.showPicture()
					pending = false
				}
				a.tileFrame(ev.Data)
			}
		case viewer.EventMessage:
			a.hostMessage(ev.Msg)
		case viewer.EventStatus:
			a.releaseHeld()
			a.connected = false
			a.showNotice(ev.Text, time.Hour)
			a.setTitle("Remoter · " + ev.Text)
		case viewer.EventEnded:
			a.disconnect(ev.Text)
		}
	}
}

// videoFrame decodes an H.264 frame and reports whether it finished a
// picture. Every frame is acknowledged, shown or not.
func (a *app) videoFrame(data []byte) bool {
	v, err := stream.ParseVideo(data)
	if err != nil {
		return false
	}
	a.acks = append(a.acks, v.Seq)
	if a.dec == nil || a.needKey && !v.Key {
		return false
	}
	a.needKey = false
	got, err := a.dec.decode(v.Frame)
	if err != nil {
		// Usually a frame lost its reference (we joined mid-stream); a key
		// frame repairs it.
		if time.Since(a.lastIDR) > time.Second {
			a.lastIDR = time.Now()
			a.client.Refresh(true)
		}
		return false
	}
	a.frames++
	return got
}

func (a *app) showPicture() {
	p, err := a.dec.picture()
	if err != nil {
		log.Print(err)
		return
	}
	if a.videoTex == 0 || a.videoSize != image.Pt(p.w, p.h) {
		if a.videoTex != 0 {
			sdl.DestroyTexture(a.videoTex)
		}
		a.videoTex = sdl.CreateTexture(a.ren, sdlPixelFormatIYUV, sdlTextureStreaming, int32(p.w), int32(p.h))
		if a.videoTex == 0 {
			log.Printf("video texture: %s", sdl.GetError())
			return
		}
		sdl.SetTextureScaleMode(a.videoTex, sdlScaleLinear)
		a.videoSize = image.Pt(p.w, p.h)
	}
	sdl.UpdateYUVTexture(a.videoTex, nil, p.planes[0], p.pitches[0], p.planes[1], p.pitches[1], p.planes[2], p.pitches[2])
	a.mode = modeVideo
	a.dirty = true
}

func (a *app) tileFrame(data []byte) {
	u, err := stream.Parse(data)
	if err != nil {
		return
	}
	a.acks = append(a.acks, u.Seq)
	a.frames++
	if a.tileTex == 0 || a.tileSize != image.Pt(u.Width, u.Height) {
		if a.tileTex != 0 {
			sdl.DestroyTexture(a.tileTex)
		}
		a.tileTex = sdl.CreateTexture(a.ren, sdlPixelFormatABGR8888, sdlTextureStreaming, int32(u.Width), int32(u.Height))
		if a.tileTex == 0 {
			log.Printf("tile texture: %s", sdl.GetError())
			return
		}
		sdl.SetTextureScaleMode(a.tileTex, sdlScaleLinear)
		a.tileSize = image.Pt(u.Width, u.Height)
	}
	imgs := make([]*image.RGBA, len(u.Rects))
	var wg sync.WaitGroup
	for i, r := range u.Rects {
		wg.Add(1)
		go func() {
			defer wg.Done()
			src, err := jpeg.Decode(bytes.NewReader(r.JPEG))
			if err != nil {
				return
			}
			img := image.NewRGBA(src.Bounds())
			draw.Draw(img, img.Rect, src, src.Bounds().Min, draw.Src)
			imgs[i] = img
		}()
	}
	wg.Wait()
	for i, r := range u.Rects {
		img := imgs[i]
		if img == nil {
			continue
		}
		rect := sdlRect{int32(r.Min.X), int32(r.Min.Y), int32(img.Rect.Dx()), int32(img.Rect.Dy())}
		sdl.UpdateTexture(a.tileTex, &rect, unsafe.Pointer(&img.Pix[0]), int32(img.Stride))
	}
	a.mode = modeTiles
	a.dirty = true
}

func (a *app) hostMessage(m protocol.Message) {
	switch m.Type {
	case protocol.TypeCursor:
		a.setCursor(m.Cursor)
	case protocol.TypeDisplays:
		a.displays, a.display = m.Count, m.Display
	case protocol.TypeClipboard:
		if m.Text != "" && m.Text != a.clip {
			a.clip = m.Text
			sdl.SetClipboardText(m.Text)
		}
	}
}

// syncClipboard sends the local clipboard to the host if it changed.
func (a *app) syncClipboard() {
	if !a.connected {
		return
	}
	if text := clipboardText(); text != "" && text != a.clip {
		a.clip = text
		a.client.Send(protocol.Message{Type: protocol.TypeClipboard, Text: text})
	}
}

func (a *app) setCursor(name string) {
	if name == "none" {
		sdl.ShowCursor(0)
		return
	}
	sdl.ShowCursor(1)
	c, ok := a.cursors[name]
	if !ok {
		id, known := systemCursors[name]
		if !known {
			id = 0
		}
		c = sdl.CreateSystemCursor(id)
		a.cursors[name] = c
	}
	if c != 0 {
		sdl.SetCursor(c)
	}
}

// capture hands all keys to the remote computer, including the desktop's own
// shortcuts (through the Wayland keyboard-shortcuts-inhibit protocol, or an
// X11 keyboard grab), and keeps the pointer inside the window.
func (a *app) capture(on bool) {
	v := int32(0)
	if on {
		v = 1
	}
	sdl.SetWindowKeyboardGrab(a.win, v)
	sdl.SetWindowMouseGrab(a.win, v)
	a.captured = on
}

func (a *app) sessionKey(ev *sdlEvent, down bool) {
	sc, sym, mod := ev.i32(16), ev.i32(20), ev.u16(24)
	if !down && a.swallow[sc] {
		delete(a.swallow, sc)
		return
	}
	// Viewer shortcuts: Ctrl+Alt plus a key the remote rarely needs.
	if down && mod&kmodCtrl != 0 && mod&kmodAlt != 0 && a.shortcut(sc) {
		a.swallow[sc] = true
		return
	}
	if !a.connected {
		return
	}
	code := viewer.CodeForScancode(int(sc))
	if code == "" {
		return
	}
	if !down {
		if key, ok := a.held[code]; ok {
			delete(a.held, code)
			a.client.Input(protocol.Input{Kind: "keyup", Key: key, Code: code})
		}
		return
	}
	if code == "KeyV" && mod&kmodCtrl != 0 {
		// Make sure the remote paste uses what was copied here.
		a.syncClipboard()
	}
	key := ""
	if sym >= 0x20 && sym < 0x40000000 && sym != 0x7f {
		key = string(rune(sym))
	}
	a.held[code] = key
	a.client.Input(protocol.Input{Kind: "keydown", Key: key, Code: code})
}

// shortcut runs a viewer shortcut and reports whether sc is one.
func (a *app) shortcut(sc int32) bool {
	switch sc {
	case scEscape:
		// Ctrl and Alt are down on the remote now; let go of them there.
		a.releaseHeld()
		if a.captured {
			a.capture(false)
			a.showNotice("Released  ·  click the screen to give the remote computer keyboard and mouse again", 6*time.Second)
		} else {
			a.capture(true)
			a.showNotice("Keyboard and mouse go to the remote computer  ·  Ctrl+Alt+Esc releases them", 3*time.Second)
		}
	case scEnter:
		a.releaseHeld()
		if sdl.GetWindowFlags(a.win)&sdlWindowFullscreenDesktop == sdlWindowFullscreenDesktop {
			sdl.SetWindowFullscreen(a.win, 0)
		} else {
			sdl.SetWindowFullscreen(a.win, sdlWindowFullscreenDesktop)
		}
	case scPageDown, scPageUp:
		if a.displays < 2 || !a.connected {
			return true
		}
		step := 1
		if sc == scPageUp {
			step = a.displays - 1
		}
		a.releaseHeld()
		a.display = (a.display + step) % a.displays
		a.client.Send(protocol.Message{Type: protocol.TypeSelectDisplay, Display: a.display})
		a.showNotice(fmt.Sprintf("Display %d of %d", a.display+1, a.displays), 2*time.Second)
	default:
		return false
	}
	return true
}

func (a *app) mouseButton(sdlButton int, down bool) {
	if a.client == nil {
		if down && sdlButton == 1 {
			a.formClick(int(a.mouse[0]), int(a.mouse[1]))
		}
		return
	}
	if !a.connected {
		return
	}
	if down && !a.captured {
		a.capture(true)
		a.showNotice("Keyboard and mouse go to the remote computer  ·  Ctrl+Alt+Esc releases them", 3*time.Second)
	}
	// SDL numbers buttons 1 left, 2 middle, 3 right; the host uses the
	// browser's 0, 1, 2.
	button := sdlButton - 1
	if button < 0 || button > 2 {
		return
	}
	if !down && !a.buttons[button] {
		return
	}
	a.flushMove()
	x, y := a.remotePoint()
	kind := "mouseup"
	if down {
		kind = "mousedown"
	}
	a.buttons[button] = down
	a.client.Input(protocol.Input{Kind: kind, Button: button, X: x, Y: y})
}

// flushMove sends the latest pointer position, once per pass of the event
// loop however many motion events arrived.
func (a *app) flushMove() {
	if !a.moved || !a.connected {
		a.moved = false
		return
	}
	a.moved = false
	x, y := a.remotePoint()
	a.client.Input(protocol.Input{Kind: "mousemove", X: x, Y: y})
}

func (a *app) remotePoint() (float64, float64) {
	return viewer.Normalize(a.dst, a.mouse[0]*a.scale, a.mouse[1]*a.scale)
}

// releaseHeld lets go of every key and button still down on the remote,
// for when their release will not reach us (focus moved away).
func (a *app) releaseHeld() {
	if a.client == nil || !a.connected {
		clear(a.held)
		clear(a.buttons)
		return
	}
	for code, key := range a.held {
		a.client.Input(protocol.Input{Kind: "keyup", Key: key, Code: code})
	}
	clear(a.held)
	x, y := a.remotePoint()
	for b, down := range a.buttons {
		if down {
			a.client.Input(protocol.Input{Kind: "mouseup", Button: b, X: x, Y: y})
		}
	}
	clear(a.buttons)
}

// ---- drawing ----

func (a *app) showNotice(text string, d time.Duration) {
	if text != a.notice && a.noticeTex != 0 {
		sdl.DestroyTexture(a.noticeTex)
		a.noticeTex = 0
	}
	a.notice, a.noticeUntil = text, time.Now().Add(d)
	a.dirty = true
}

func (a *app) setTitle(t string) {
	if t != a.title {
		a.title = t
		sdl.SetWindowTitle(a.win, t)
	}
}

// tick does the once-a-second work: frame rate in the title, notice expiry.
func (a *app) tick() {
	now := time.Now()
	if a.notice != "" && now.After(a.noticeUntil) {
		a.showNotice("", 0)
	}
	if now.Sub(a.statsAt) < time.Second {
		return
	}
	if a.connected {
		codec := "H.264"
		if a.mode == modeTiles {
			codec = "JPEG"
		}
		secs := now.Sub(a.statsAt).Seconds()
		a.setTitle(fmt.Sprintf("Remoter · %s · %.0f fps · %.1f Mbit/s · %s", a.server,
			float64(a.frames)/secs, float64(a.bytes)*8/1e6/secs, codec))
	}
	a.frames, a.bytes, a.statsAt = 0, 0, now
}

func (a *app) render() {
	a.dirty = false
	var ow, oh, ww, wh int32
	sdl.GetRendererOutputSize(a.ren, &ow, &oh)
	sdl.GetWindowSize(a.win, &ww, &wh)
	out := image.Pt(int(ow), int(oh))
	if ww > 0 {
		a.scale = float64(ow) / float64(ww)
	}
	sdl.SetRenderDrawColor(a.ren, 0, 0, 0, 0xff)
	sdl.RenderClear(a.ren)

	if a.client == nil {
		a.drawForm(out)
	} else {
		tex, size := uintptr(0), image.Point{}
		switch a.mode {
		case modeVideo:
			tex, size = a.videoTex, a.videoSize
		case modeTiles:
			tex, size = a.tileTex, a.tileSize
		}
		if tex != 0 {
			a.dst = viewer.Fit(size.X, size.Y, out)
			d := sdlRect{int32(a.dst.Min.X), int32(a.dst.Min.Y), int32(a.dst.Dx()), int32(a.dst.Dy())}
			sdl.RenderCopy(a.ren, tex, nil, &d)
		}
	}

	if a.notice != "" {
		if a.noticeTex == 0 {
			img := noticeImage(a.notice, a.scale)
			a.noticeTex, a.noticeSize = a.texture(img), img.Rect.Size()
		}
		if a.noticeTex != 0 {
			d := sdlRect{int32((out.X - a.noticeSize.X) / 2), int32(16 * a.scale), int32(a.noticeSize.X), int32(a.noticeSize.Y)}
			sdl.RenderCopy(a.ren, a.noticeTex, nil, &d)
		}
	}
	sdl.RenderPresent(a.ren)
}

func (a *app) drawForm(out image.Point) {
	if a.formTex != 0 {
		sdl.DestroyTexture(a.formTex)
	}
	img := a.form.draw(out, a.scale)
	a.formTex = a.texture(img)
	d := sdlRect{0, 0, int32(out.X), int32(out.Y)}
	sdl.RenderCopy(a.ren, a.formTex, nil, &d)
}

// texture uploads img as a new texture.
func (a *app) texture(img *image.RGBA) uintptr {
	r := img.Rect
	if r.Empty() {
		return 0
	}
	tex := sdl.CreateTexture(a.ren, sdlPixelFormatABGR8888, sdlTextureStatic, int32(r.Dx()), int32(r.Dy()))
	if tex == 0 {
		return 0
	}
	sdl.SetTextureBlendMode(tex, sdlBlendBlend)
	sdl.UpdateTexture(tex, nil, unsafe.Pointer(&img.Pix[0]), int32(img.Stride))
	return tex
}

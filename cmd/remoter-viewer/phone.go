//go:build linux || windows

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"
	"unsafe"

	"github.com/aleynikovich/remoter/internal/phone"
	"github.com/aleynikovich/remoter/internal/viewer"
)

// The phone window mirrors an Android phone and controls it with the mouse
// and keyboard (see internal/phone). It runs as its own process,
// `remoter-viewer phone <ip>`, so it opens next to the computer viewer.

// Scancodes of the phone window's Ctrl+Alt shortcuts.
const (
	scB = 5
	scH = 11
	scN = 17
	scO = 18
	scP = 19
	scR = 21
	scS = 22
)

const kmodLAlt = 0x0100

const phoneHelp = "Click: tap · right-click: back · middle-click: home\nCtrl+Alt+ S screenshot · H home · B back · R recent apps · N notifications · P power · O phone screen off/on · ↑↓ volume · Enter full screen"

const (
	phoneConnecting = iota
	phonePairing
	phoneLive
	phoneEnded
)

// phoneEvent is the result of work done off the window's thread.
type phoneEvent struct {
	session *phone.Session
	serial  string
	pair    bool // the phone needs pairing or its port
	err     error
	shot    string // a screenshot was saved here
	shotErr error
	isShot  bool
}

type phoneApp struct {
	win, ren uintptr
	wakeType uint32
	woken    atomic.Bool
	quit     bool

	cfg     viewer.Config
	cfgPath string
	ip      string
	adb     *phone.ADB
	serial  string

	state   int
	status  string // what the connecting or ended screen says
	form    *form
	formTex uintptr
	work    chan phoneEvent
	busy    bool // pairing or connecting in the background
	asked   bool // the pairing form was shown

	sess     *phone.Session
	sessEvs  chan phone.Event
	newDec   func() (videoDecoder, error)
	dec      videoDecoder
	needKey  bool
	lastIDR  time.Time
	videoTex uintptr
	texSize  image.Point
	size     image.Point // the phone's video size, which touches are measured in
	sized    bool        // the window was fitted to the phone's shape

	touching bool
	moved    bool
	mouse    [2]float64
	dst      image.Rectangle
	scale    float64
	held     map[int32]int // scancode -> Android key code down on the phone
	swallow  map[int32]bool
	clip     string
	screenOn bool
	shade    bool
	shooting bool

	notice      string
	noticeUntil time.Time
	noticeTex   uintptr
	noticeSize  image.Point

	frames  int
	statsAt time.Time
	title   string
	dirty   bool
}

// phoneMain runs `remoter-viewer phone [ip]`.
func phoneMain(args []string) {
	setupLog()
	cfgPath, err := viewer.ConfigPath()
	if err != nil {
		log.Printf("settings will not be saved: %v", err)
	}
	cfg := viewer.LoadConfig(cfgPath)
	ip := cfg.Phone
	if len(args) > 0 {
		ip = args[0]
	}
	if ip == "" {
		fatal(errors.New("usage: remoter-viewer phone <phone's Tailscale IP>"))
	}
	if err := loadSDL(); err != nil {
		fatal(err)
	}
	newDec, err := videoDecoders()
	if err != nil {
		fatal(fmt.Errorf("no H.264 decoder for the phone's video: %w", err))
	}
	a, err := newPhoneApp(cfg, cfgPath, ip, newDec)
	if err != nil {
		fatal(err)
	}
	a.run()
}

// phoneShotMain runs `remoter-viewer phone-shot [ip]`: one screenshot to the
// clipboard and Pictures/Remoter, without a window, for a desktop hotkey.
func phoneShotMain(args []string) {
	cfgPath, _ := viewer.ConfigPath()
	cfg := viewer.LoadConfig(cfgPath)
	ip := cfg.Phone
	if len(args) > 0 {
		ip = args[0]
	}
	err := func() error {
		if ip == "" {
			return errors.New("open the phone in the viewer once first, or pass its IP")
		}
		a, err := phone.FindADB()
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		serial, err := a.Link(ctx, ip, cfg.PhonePort)
		if err != nil {
			return fmt.Errorf("reach the phone: %w", err)
		}
		path, err := screenshot(ctx, a, serial)
		if path != "" {
			fmt.Println(path)
		}
		if err == nil {
			notify("Phone screenshot copied", filepath.Base(path))
		}
		return err
	}()
	if err != nil {
		notify("Phone screenshot failed", err.Error())
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// screenshot saves the phone's screen and copies it. A failed copy still
// returns the saved file.
func screenshot(ctx context.Context, a *phone.ADB, serial string) (string, error) {
	png, err := a.Screenshot(ctx, serial)
	if err != nil {
		return "", err
	}
	path, err := phone.SaveScreenshot(png)
	if err != nil {
		return "", err
	}
	if err := phone.CopyImage(png); err != nil {
		return path, fmt.Errorf("saved %s, but not copied: %w", filepath.Base(path), err)
	}
	return path, nil
}

// notify shows a desktop notification where there is a way to.
func notify(title, body string) {
	if runtime.GOOS == "linux" {
		exec.Command("notify-send", "--app-name=Remoter", "--expire-time=3000", title, body).Run()
	}
}

// openPhoneWindow starts the phone window as a process of its own.
func openPhoneWindow(ip string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	c := exec.Command(exe, "phone", ip)
	if err := c.Start(); err != nil {
		return err
	}
	go c.Wait()
	return nil
}

func newPhoneApp(cfg viewer.Config, cfgPath, ip string, newDec func() (videoDecoder, error)) (*phoneApp, error) {
	sdl.SetHint("SDL_VIDEO_WAYLAND_WMCLASS", "remoter-phone")
	sdl.SetHint("SDL_VIDEO_X11_WMCLASS", "remoter-phone")
	sdl.SetHint("SDL_APP_NAME", "Remoter")
	sdl.SetHint("SDL_MOUSE_FOCUS_CLICKTHROUGH", "1")
	sdl.SetHint("SDL_RENDER_SCALE_QUALITY", "linear")
	sdl.SetHint("SDL_RENDER_VSYNC", "0")
	sdl.SetHint("SDL_VIDEO_ALLOW_SCREENSAVER", "1")
	sdl.SetHint("SDL_WINDOWS_DPI_AWARENESS", "permonitorv2")
	sdl.SetHint("SDL_WINDOWS_DPI_SCALING", "1")
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
	win := sdl.CreateWindow("Remoter · phone", sdlWindowPosCentered, sdlWindowPosCentered, 560, 900, sdlWindowResizable|sdlWindowAllowHighDPI)
	if win == 0 {
		return nil, fmt.Errorf("open window: %s", sdl.GetError())
	}
	sdl.SetWindowMinimumSize(win, 240, 240)
	ren := sdl.CreateRenderer(win, -1, sdlRendererAccelerated)
	if ren == 0 {
		return nil, fmt.Errorf("start renderer: %s", sdl.GetError())
	}
	// Android's encoders tag their video BT.601 at phone sizes.
	sdl.SetYUVConversionMode(sdlYUVBT601)
	sdl.SetRenderDrawBlendMode(ren, sdlBlendBlend)
	a := &phoneApp{
		win: win, ren: ren,
		wakeType: sdl.RegisterEvents(1),
		cfg:      cfg, cfgPath: cfgPath, ip: ip,
		work:    make(chan phoneEvent, 4),
		sessEvs: make(chan phone.Event, 64),
		newDec:  newDec,
		held:    map[int32]int{},
		swallow: map[int32]bool{},
		scale:   1,
		dirty:   true,
	}
	a.form = &form{
		heading: "Pair your phone",
		sub:     "Once, so this computer may mirror it",
		lines: []string{
			"1. On the phone: Settings › Developer options › Wireless debugging, turn it on.",
			"2. Type the port shown after the colon under \"IP address & Port\".",
			"3. First time only: tap \"Pair device with pairing code\" and type that dialog's port and code too.",
			"Then allow this computer if the phone asks.",
		},
		button: "Connect",
		hint:   "Tab: next field  ·  Enter: connect  ·  Esc: try again without pairing",
		secret: -1,
		labels: [3]string{"Wireless debugging port (after the colon)", "Pairing port (first time only)", "Pairing code (first time only)"},
	}
	if cfg.PhonePort != phone.TCPPort {
		a.form.values[0] = cfg.PhonePort
	}
	sdl.StartTextInput()
	a.connect()
	return a, nil
}

func (a *phoneApp) wakeUp() {
	if a.woken.Swap(true) {
		return
	}
	var ev sdlEvent
	*(*uint32)(unsafe.Pointer(&ev[0])) = a.wakeType
	sdl.PushEvent(&ev)
}

func (a *phoneApp) run() {
	var ev sdlEvent
	for !a.quit {
		if sdl.WaitEventTimeout(&ev, 250) != 0 {
			a.handle(&ev)
			for sdl.PollEvent(&ev) != 0 {
				a.handle(&ev)
			}
		}
		a.woken.Store(false)
		a.drainWork()
		a.drainSession()
		a.flushMove()
		a.tick()
		if a.dirty {
			a.render()
		}
	}
	if a.sess != nil {
		a.sess.Close()
	}
	sdl.Quit()
}

// ---- connecting ----

// connect reaches the phone with what worked last time, asking for pairing
// when that fails.
func (a *phoneApp) connect() {
	a.state, a.status, a.busy = phoneConnecting, "Connecting to the phone at "+a.ip+"…", true
	a.setTitle("Remoter · connecting to the phone")
	a.dirty = true
	go func() {
		ev := a.reach(func(ctx context.Context) (string, error) {
			return a.adb.Link(ctx, a.ip, a.cfg.PhonePort)
		})
		a.work <- ev
		a.wakeUp()
	}()
}

// pair runs the pairing form: pairs if a code was typed, then connects on
// the wireless debugging port.
func (a *phoneApp) pair() {
	port := strings.TrimSpace(a.form.values[0])
	pairPort, code := strings.TrimSpace(a.form.values[1]), strings.TrimSpace(a.form.values[2])
	if port == "" {
		a.form.status, a.form.err = "Type the wireless debugging port first", true
		a.dirty = true
		return
	}
	a.state, a.status, a.busy = phoneConnecting, "Pairing with the phone…", true
	a.dirty = true
	go func() {
		ev := a.reach(func(ctx context.Context) (string, error) {
			if code != "" {
				if pairPort == "" {
					return "", errors.New("type the pairing dialog's port too")
				}
				if err := a.adb.Pair(ctx, net.JoinHostPort(a.ip, pairPort), code); err != nil {
					return "", err
				}
			}
			return a.adb.LinkPort(ctx, a.ip, port)
		})
		if ev.err == nil {
			a.cfg.PhonePort = port
		}
		a.work <- ev
		a.wakeUp()
	}()
}

// reach finds adb, connects with link and starts mirroring.
func (a *phoneApp) reach(link func(ctx context.Context) (string, error)) phoneEvent {
	if a.adb == nil {
		adb, err := phone.FindADB()
		if err != nil {
			return phoneEvent{err: err}
		}
		a.adb = adb
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	serial, err := link(ctx)
	if err != nil {
		// Unauthorized needs a tap on the phone, not pairing.
		return phoneEvent{err: err, pair: !errors.Is(err, phone.ErrUnauthorized)}
	}
	s, err := phone.Start(ctx, a.adb, serial, phone.Options{
		BitRate: 12_000_000, MaxFPS: 60, Baseline: openH264Only,
	})
	if err != nil {
		return phoneEvent{err: err, serial: serial}
	}
	return phoneEvent{session: s, serial: serial}
}

func (a *phoneApp) drainWork() {
	for {
		select {
		case ev := <-a.work:
			a.workDone(ev)
		default:
			return
		}
	}
}

func (a *phoneApp) workDone(ev phoneEvent) {
	a.dirty = true
	if ev.isShot {
		a.shooting = false
		switch {
		case ev.shotErr != nil:
			a.showNotice(ev.shotErr.Error(), 6*time.Second)
		default:
			a.showNotice("Screenshot copied  ·  saved as "+filepath.Base(ev.shot)+" in Pictures/Remoter", 4*time.Second)
		}
		return
	}
	a.busy = false
	if ev.err != nil {
		log.Printf("phone: %v", ev.err)
		if ev.pair {
			a.state = phonePairing
			a.form.status, a.form.err = ev.err.Error(), true
			if a.cfg.PhonePort == "" && !a.asked {
				// Never paired: nothing went wrong yet.
				a.form.status, a.form.err = "", false
			}
			a.asked = true
			a.form.focus = 0
			a.setTitle("Remoter · pair the phone")
		} else {
			a.state, a.status = phoneEnded, ev.err.Error()
			a.setTitle("Remoter · phone")
		}
		return
	}
	a.serial, a.sess = ev.serial, ev.session
	a.cfg.Phone = a.ip
	if a.cfgPath != "" {
		// The main viewer may have saved in the meantime; keep its part.
		cur := viewer.LoadConfig(a.cfgPath)
		cur.Phone, cur.PhonePort = a.cfg.Phone, a.cfg.PhonePort
		if err := cur.Save(a.cfgPath); err != nil {
			log.Printf("save settings: %v", err)
		}
	}
	a.state, a.screenOn, a.shade = phoneLive, true, false
	a.startDecoder()
	sess, out := a.sess, a.sessEvs
	go func() {
		for ev := range sess.Events() {
			out <- ev
			a.wakeUp()
			if ev.Kind == phone.EventEnded {
				return
			}
		}
	}()
	sdl.StartTextInput()
	a.setTitle("Remoter · " + a.sess.Name)
	a.showNotice(phoneHelp, 8*time.Second)
}

// ---- mirroring ----

func (a *phoneApp) startDecoder() {
	if a.dec != nil {
		a.dec.close()
		a.dec = nil
	}
	dec, err := a.newDec()
	if err != nil {
		log.Printf("decoder: %v", err)
		return
	}
	a.dec, a.needKey = dec, true
}

func (a *phoneApp) drainSession() {
	pending := false
	for {
		var ev phone.Event
		select {
		case ev = <-a.sessEvs:
		default:
			if pending {
				a.showPicture()
			}
			return
		}
		if a.sess == nil {
			continue // from a session already closed
		}
		switch ev.Kind {
		case phone.EventSize:
			a.size = image.Pt(ev.Width, ev.Height)
			a.fitWindow()
		case phone.EventFrame:
			pending = a.decode(ev) || pending
		case phone.EventClipboard:
			if ev.Text != "" && ev.Text != a.clip {
				a.clip = ev.Text
				sdl.SetClipboardText(ev.Text)
			}
		case phone.EventEnded:
			a.endSession(ev.Err)
			return
		}
	}
}

func (a *phoneApp) decode(ev phone.Event) bool {
	if a.dec == nil || a.needKey && !ev.Key {
		return false
	}
	a.needKey = false
	got, err := a.dec.decode(ev.Data)
	if err != nil {
		if time.Since(a.lastIDR) > time.Second {
			a.lastIDR = time.Now()
			a.sess.ResetVideo()
		}
		return false
	}
	if got {
		a.frames++
	}
	return got
}

func (a *phoneApp) showPicture() {
	p, err := a.dec.picture()
	if err != nil {
		return
	}
	if a.videoTex == 0 || a.texSize != image.Pt(p.w, p.h) {
		if a.videoTex != 0 {
			sdl.DestroyTexture(a.videoTex)
		}
		a.videoTex = sdl.CreateTexture(a.ren, sdlPixelFormatIYUV, sdlTextureStreaming, int32(p.w), int32(p.h))
		if a.videoTex == 0 {
			log.Printf("video texture: %s", sdl.GetError())
			return
		}
		sdl.SetTextureScaleMode(a.videoTex, sdlScaleLinear)
		a.texSize = image.Pt(p.w, p.h)
	}
	sdl.UpdateYUVTexture(a.videoTex, nil, p.planes[0], p.pitches[0], p.planes[1], p.pitches[1], p.planes[2], p.pitches[2])
	a.dirty = true
}

// fitWindow sizes the window to the phone's shape, as tall as the screen
// allows, when it starts and when the phone rotates or unfolds.
func (a *phoneApp) fitWindow() {
	if sdl.GetWindowFlags(a.win)&sdlWindowFullscreenDesktop == sdlWindowFullscreenDesktop {
		return
	}
	var r sdlRect
	if sdl.GetDisplayUsableBounds(sdl.GetWindowDisplayIndex(a.win), &r) != 0 || r.W <= 0 || r.H <= 0 {
		return
	}
	fit := viewer.Fit(a.size.X, a.size.Y, image.Pt(int(r.W)*9/10, int(r.H)*9/10))
	if fit.Empty() {
		return
	}
	sdl.SetWindowSize(a.win, int32(fit.Dx()), int32(fit.Dy()))
	if !a.sized {
		sdl.SetWindowPosition(a.win, sdlWindowPosCentered, sdlWindowPosCentered)
	}
	a.sized = true
	a.dirty = true
}

func (a *phoneApp) endSession(err error) {
	a.releaseHeld()
	if a.sess != nil {
		a.sess.Close()
		a.sess = nil
	}
	if a.dec != nil {
		a.dec.close()
		a.dec = nil
	}
	a.state = phoneEnded
	a.status = "The phone disconnected"
	if err != nil {
		a.status += ": " + err.Error()
	}
	a.setTitle("Remoter · phone disconnected")
	a.dirty = true
}

// ---- input ----

func (a *phoneApp) handle(ev *sdlEvent) {
	switch ev.typ() {
	case sdlQuit:
		a.quit = true
	case sdlWindowEvent:
		if ev.u8(12) == sdlWindowEventFocusLost {
			a.releaseHeld()
		}
		a.dirty = true
	case sdlRenderReset, sdlRenderReset + 1:
		a.videoTex, a.formTex, a.noticeTex = 0, 0, 0
		a.texSize = image.Point{}
		if a.sess != nil {
			a.sess.ResetVideo()
		}
		a.dirty = true
	case sdlKeyDown, sdlKeyUp:
		down := ev.typ() == sdlKeyDown
		switch a.state {
		case phoneLive:
			a.liveKey(ev, down)
		case phonePairing:
			if down {
				a.formKey(ev)
			}
		case phoneEnded:
			if down && (ev.i32(16) == scEnter || ev.i32(16) == scKPEnter) {
				a.connect()
			}
		}
	case sdlTextInput:
		text := ev[12:44]
		if i := bytes.IndexByte(text, 0); i >= 0 {
			text = text[:i]
		}
		switch {
		case a.state == phoneLive && a.sess != nil:
			a.sess.Text(string(text))
		case a.state == phonePairing && a.form.focus < 3:
			a.form.values[a.form.focus] += string(text)
			a.form.status, a.form.err = "", false
			a.dirty = true
		}
	case sdlMouseMotion:
		a.mouse = [2]float64{float64(ev.i32(20)), float64(ev.i32(24))}
		a.moved = a.touching
	case sdlMouseButtonDown, sdlMouseButtonUp:
		a.mouse = [2]float64{float64(ev.i32(20)), float64(ev.i32(24))}
		a.mouseButton(int(ev.u8(16)), ev.typ() == sdlMouseButtonDown)
	case sdlMouseWheel:
		if a.sess == nil {
			return
		}
		x, y := float64(ev.f32(28)), float64(ev.f32(32))
		if x == 0 && y == 0 {
			x, y = float64(ev.i32(16)), float64(ev.i32(20))
		}
		if ev.u32(24) == sdlMouseWheelFlipped {
			x, y = -x, -y
		}
		nx, ny := a.point()
		a.sess.Scroll(nx, ny, x, y)
	}
}

func (a *phoneApp) point() (float64, float64) {
	return viewer.Normalize(a.dst, a.mouse[0]*a.scale, a.mouse[1]*a.scale)
}

func (a *phoneApp) mouseButton(button int, down bool) {
	if a.state == phonePairing && down && button == 1 {
		p := image.Pt(int(a.mouse[0]*a.scale), int(a.mouse[1]*a.scale))
		for i, r := range a.form.hit {
			if p.In(r) {
				a.form.focus = i
				a.dirty = true
				if i == 3 {
					a.pair()
				}
			}
		}
		return
	}
	if a.state == phoneEnded && down && button == 1 {
		a.connect()
		return
	}
	if a.sess == nil {
		return
	}
	switch button {
	case 1: // left: a finger
		if down == a.touching {
			return
		}
		a.flushMove()
		nx, ny := a.point()
		a.touching = down
		if down {
			a.sess.Touch(phone.ActionDown, nx, ny)
		} else {
			a.sess.Touch(phone.ActionUp, nx, ny)
		}
	case 3: // right: back, as in scrcpy
		if down {
			a.sess.Back()
		}
	case 2: // middle: home
		if down {
			a.sess.Press(phone.KeyHome)
		}
	case 4: // mouse "back" side button
		if down {
			a.sess.Press(phone.KeyBack)
		}
	case 5:
		if down {
			a.sess.Press(phone.KeyAppSwitch)
		}
	}
}

// flushMove sends the finger's latest position once per pass of the loop.
func (a *phoneApp) flushMove() {
	if !a.moved || !a.touching || a.sess == nil {
		a.moved = false
		return
	}
	a.moved = false
	nx, ny := a.point()
	a.sess.Touch(phone.ActionMove, nx, ny)
}

func (a *phoneApp) liveKey(ev *sdlEvent, down bool) {
	sc, mod, repeat := ev.i32(16), ev.u16(24), ev.u8(13) != 0
	if !down && a.swallow[sc] {
		delete(a.swallow, sc)
		return
	}
	if down && mod&kmodCtrl != 0 && mod&kmodAlt != 0 && a.shortcut(sc) {
		a.swallow[sc] = true
		return
	}
	if a.sess == nil {
		return
	}
	if !down {
		if key, ok := a.held[sc]; ok {
			delete(a.held, sc)
			a.sess.Key(phone.ActionUp, key, 0, meta(mod))
		}
		return
	}
	key, typed := phone.Keycode(int(sc))
	if key == 0 {
		return
	}
	// Characters arrive as text, which follows the PC's keyboard layout;
	// with Ctrl (or Alt, or the Windows key) they are shortcuts instead.
	shortcut := mod&(kmodCtrl|kmodLAlt|kmodGUI) != 0
	if typed && !shortcut {
		return
	}
	if sc == scV && mod&kmodCtrl != 0 && mod&(kmodLAlt|kmodGUI) == 0 {
		// Paste what was copied here, not the phone's own clipboard.
		if text := clipboardText(); text != "" {
			a.clip = text
			a.sess.Paste(text)
			a.swallow[sc] = true
			return
		}
	}
	n := 0
	if repeat {
		n = 1
	}
	a.held[sc] = key
	a.sess.Key(phone.ActionDown, key, n, meta(mod))
}

// meta turns SDL modifiers into Android meta state.
func meta(mod uint16) int {
	m := 0
	if mod&kmodShift != 0 {
		m |= phone.MetaShift
	}
	if mod&kmodCtrl != 0 {
		m |= phone.MetaCtrl
	}
	if mod&kmodAlt != 0 {
		m |= phone.MetaAlt
	}
	if mod&kmodGUI != 0 {
		m |= phone.MetaMeta
	}
	return m
}

func (a *phoneApp) shortcut(sc int32) bool {
	switch sc {
	case scEnter:
		a.releaseHeld()
		if sdl.GetWindowFlags(a.win)&sdlWindowFullscreenDesktop == sdlWindowFullscreenDesktop {
			sdl.SetWindowFullscreen(a.win, 0)
		} else {
			sdl.SetWindowFullscreen(a.win, sdlWindowFullscreenDesktop)
		}
		return true
	case scS:
		a.screenshot()
		return true
	}
	if a.sess == nil {
		return false
	}
	a.releaseHeld()
	switch sc {
	case scH:
		a.sess.Press(phone.KeyHome)
	case scB:
		a.sess.Back()
	case scR:
		a.sess.Press(phone.KeyAppSwitch)
	case scP:
		a.sess.Press(phone.KeyPower)
	case scUp:
		a.sess.Press(phone.KeyVolumeUp)
	case scDown:
		a.sess.Press(phone.KeyVolumeDown)
	case scN:
		a.shade = !a.shade
		a.sess.Notifications(a.shade)
	case scO:
		a.screenOn = !a.screenOn
		a.sess.DisplayPower(a.screenOn)
		if a.screenOn {
			a.showNotice("Phone screen on", 2*time.Second)
		} else {
			a.showNotice("Phone screen off  ·  mirroring goes on  ·  Ctrl+Alt+O turns it back on", 4*time.Second)
		}
	default:
		return false
	}
	return true
}

// screenshot saves the phone's screen at full resolution in the background.
func (a *phoneApp) screenshot() {
	if a.serial == "" || a.adb == nil || a.shooting {
		return
	}
	a.shooting = true
	a.showNotice("Taking a screenshot…", 10*time.Second)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		path, err := screenshot(ctx, a.adb, a.serial)
		a.work <- phoneEvent{isShot: true, shot: path, shotErr: err}
		a.wakeUp()
	}()
}

func (a *phoneApp) releaseHeld() {
	if a.sess != nil {
		if a.touching {
			nx, ny := a.point()
			a.sess.Touch(phone.ActionUp, nx, ny)
		}
		for _, key := range a.held {
			a.sess.Key(phone.ActionUp, key, 0, 0)
		}
	}
	a.touching = false
	clear(a.held)
}

func (a *phoneApp) formKey(ev *sdlEvent) {
	sc, mod := ev.i32(16), ev.u16(24)
	f := a.form
	switch {
	case sc == scTab && mod&kmodShift != 0:
		f.focus = (f.focus + 3) % 4
	case sc == scTab:
		f.focus = (f.focus + 1) % 4
	case sc == scEnter || sc == scKPEnter:
		a.pair()
		return
	case sc == scEscape:
		a.connect()
		return
	case sc == scBack && f.focus < 3:
		v := f.values[f.focus]
		_, size := utf8.DecodeLastRuneInString(v)
		f.values[f.focus] = v[:len(v)-size]
	case sc == scV && mod&kmodCtrl != 0 && f.focus < 3:
		f.values[f.focus] += strings.TrimSpace(clipboardText())
	default:
		return
	}
	f.status, f.err = "", false
	a.dirty = true
}

// ---- drawing ----

func (a *phoneApp) showNotice(text string, d time.Duration) {
	if text != a.notice && a.noticeTex != 0 {
		sdl.DestroyTexture(a.noticeTex)
		a.noticeTex = 0
	}
	a.notice, a.noticeUntil = text, time.Now().Add(d)
	a.dirty = true
}

func (a *phoneApp) setTitle(t string) {
	if t != a.title {
		a.title = t
		sdl.SetWindowTitle(a.win, t)
	}
}

func (a *phoneApp) tick() {
	now := time.Now()
	if a.notice != "" && now.After(a.noticeUntil) {
		a.showNotice("", 0)
	}
	if now.Sub(a.statsAt) < time.Second {
		return
	}
	if a.state == phoneLive && a.sess != nil {
		a.setTitle(fmt.Sprintf("Remoter · %s · %.0f fps", a.sess.Name, float64(a.frames)/now.Sub(a.statsAt).Seconds()))
	}
	a.frames, a.statsAt = 0, now
}

func (a *phoneApp) render() {
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

	switch {
	case a.state == phoneLive && a.videoTex != 0:
		a.dst = viewer.Fit(a.size.X, a.size.Y, out)
		d := sdlRect{int32(a.dst.Min.X), int32(a.dst.Min.Y), int32(a.dst.Dx()), int32(a.dst.Dy())}
		sdl.RenderCopy(a.ren, a.videoTex, nil, &d)
	case a.state == phonePairing:
		a.drawPage(a.form.draw(out, a.scale))
	case a.state == phoneConnecting || a.state == phoneEnded:
		a.drawPage(a.statusPage(out))
	}

	if a.notice != "" {
		if a.noticeTex == 0 {
			img := wrappedNotice(a.notice, a.scale, out.X-int(16*a.scale))
			a.noticeTex, a.noticeSize = textureOf(a.ren, img), img.Rect.Size()
		}
		if a.noticeTex != 0 {
			d := sdlRect{int32(max((out.X-a.noticeSize.X)/2, 0)), int32(16 * a.scale), int32(a.noticeSize.X), int32(a.noticeSize.Y)}
			sdl.RenderCopy(a.ren, a.noticeTex, nil, &d)
		}
	}
	sdl.RenderPresent(a.ren)
}

func (a *phoneApp) drawPage(img *image.RGBA) {
	if a.formTex != 0 {
		sdl.DestroyTexture(a.formTex)
	}
	a.formTex = textureOf(a.ren, img)
	d := sdlRect{0, 0, int32(img.Rect.Dx()), int32(img.Rect.Dy())}
	sdl.RenderCopy(a.ren, a.formTex, nil, &d)
}

// statusPage says what the window is doing while there is no picture.
func (a *phoneApp) statusPage(out image.Point) *image.RGBA {
	img := image.NewRGBA(image.Rectangle{Max: out})
	fill(img, img.Rect, colBackground)
	px := func(v float64) int { return int(v*a.scale + 0.5) }
	title, small := face(px(24)), face(px(14))
	x, w := px(24), out.X-px(48)
	y := out.Y/2 - px(40)
	drawText(img, title, x, y, "Phone", colText)
	y += px(32)
	c := colMuted
	if a.state == phoneEnded {
		c = colError
	}
	for _, l := range wrap(small, a.status, w) {
		drawText(img, small, x, y, l, c)
		y += px(20)
	}
	if a.state == phoneEnded {
		drawText(img, small, x, y+px(16), "Enter or click: try again", colMuted)
	}
	return img
}

// wrappedNotice is noticeImage over as many lines as a phone-shaped window
// needs.
func wrappedNotice(text string, s float64, width int) *image.RGBA {
	px := func(v float64) int { return int(v*s + 0.5) }
	f := face(px(15))
	var lines []string
	for _, part := range strings.Split(text, "\n") {
		lines = append(lines, wrap(f, part, width-px(32))...)
	}
	w := 0
	for _, l := range lines {
		w = max(w, textWidth(f, l))
	}
	r := image.Rect(0, 0, min(w+px(32), width), px(14)+len(lines)*px(22))
	img := image.NewRGBA(r)
	fill(img, r, colNotice)
	for i, l := range lines {
		drawText(img, f, px(16), px(25)+i*px(22), l, colText)
	}
	return img
}

// textureOf uploads img as a new texture.
func textureOf(ren uintptr, img *image.RGBA) uintptr {
	r := img.Rect
	if r.Empty() {
		return 0
	}
	tex := sdl.CreateTexture(ren, sdlPixelFormatABGR8888, sdlTextureStatic, int32(r.Dx()), int32(r.Dy()))
	if tex == 0 {
		return 0
	}
	sdl.SetTextureBlendMode(tex, sdlBlendBlend)
	sdl.UpdateTexture(tex, nil, unsafe.Pointer(&img.Pix[0]), int32(img.Stride))
	return tex
}

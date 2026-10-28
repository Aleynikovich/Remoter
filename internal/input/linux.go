//go:build linux

package input

import (
	"errors"
	"image"
	"log"
	"math"
	"os"
	"sync"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
	"github.com/jezek/xgb/xtest"

	"github.com/aleynikovich/remoter/internal/protocol"
	"github.com/aleynikovich/remoter/internal/wayland"
)

// device is a way to type and point on this computer.
type device interface {
	move(p image.Point)
	button(b int, down bool)
	scroll(notches float64)
	key(code uint32, down bool)
	setDisplay(display image.Rectangle)
	// bounds is the display area to use when the host did not know one yet.
	bounds() image.Rectangle
}

func newNative(display image.Rectangle) (Handler, error) {
	var dev device
	var err error
	if wayland.Available() {
		dev, err = newWaylandDevice(display)
	} else if os.Getenv("DISPLAY") != "" {
		dev, err = newX11Device()
	} else {
		err = errors.New("no Wayland compositor with virtual input, and no X11 display")
	}
	if err != nil {
		return nil, err
	}
	return &linux{
		dev:     dev,
		display: display,
		keys:    make(map[string]map[uint32]bool),
		buttons: make(map[string]map[int]bool),
	}, nil
}

type linux struct {
	dev     device
	display image.Rectangle

	mu      sync.Mutex
	keys    map[string]map[uint32]bool
	buttons map[string]map[int]bool
}

func (l *linux) HandleInput(viewer string, in protocol.Input) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.display.Empty() {
		l.display = l.dev.bounds()
		if l.display.Empty() {
			return
		}
	}
	switch in.Kind {
	case "mousemove":
		l.dev.move(toScreen(l.display, in.X, in.Y))
	case "mousedown", "mouseup":
		down := in.Kind == "mousedown"
		l.dev.move(toScreen(l.display, in.X, in.Y))
		l.dev.button(in.Button, down)
		setHeld(l.buttons, viewer, in.Button, down)
	case "wheel":
		l.dev.move(toScreen(l.display, in.X, in.Y))
		// Browsers report about 100 pixels per notch, down positive.
		notches := in.DeltaY / 100
		if math.Abs(notches) >= 0.5 {
			notches = math.Round(notches)
		}
		if notches != 0 {
			l.dev.scroll(notches)
		}
	case "keydown", "keyup":
		code, ok := evdevByCode[in.Code]
		if !ok {
			log.Printf("input: no mapping for key code=%q key=%q", in.Code, in.Key)
			return
		}
		down := in.Kind == "keydown"
		l.dev.key(code, down)
		setHeld(l.keys, viewer, code, down)
	}
}

func (l *linux) ReleaseAll(viewer string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for k := range l.keys[viewer] {
		l.dev.key(k, false)
	}
	for b := range l.buttons[viewer] {
		l.dev.button(b, false)
	}
	delete(l.keys, viewer)
	delete(l.buttons, viewer)
}

func (l *linux) SetDisplay(display image.Rectangle) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.display = display
	l.dev.setDisplay(display)
}

func setHeld[T comparable](held map[string]map[T]bool, viewer string, v T, down bool) {
	if down {
		if held[viewer] == nil {
			held[viewer] = make(map[T]bool)
		}
		held[viewer][v] = true
	} else {
		delete(held[viewer], v)
	}
}

// waylandDevice uses the virtual pointer and keyboard protocols, which
// Hyprland and other wlroots compositors offer without special permissions.
type waylandDevice struct {
	display image.Rectangle
	xkb     *xkb

	conn     *wayland.Conn
	output   wayland.Output
	pointer  *wayland.Pointer
	keyboard *wayland.Keyboard
}

func newWaylandDevice(display image.Rectangle) (*waylandDevice, error) {
	w := &waylandDevice{display: display, xkb: newXKB()}
	return w, w.ensure()
}

// ensure (re)creates the virtual devices after a reconnect or a display
// switch.
func (w *waylandDevice) ensure() error {
	c, err := wayland.Shared()
	if err != nil {
		return err
	}
	if c == w.conn && w.pointer != nil {
		return nil
	}
	if c != w.conn {
		w.pointer, w.keyboard = nil, nil
	}
	w.conn = c
	outs := c.Outputs()
	if len(outs) == 0 {
		return errors.New("no monitors")
	}
	w.output = outs[0]
	for _, o := range outs {
		if o.X == w.display.Min.X && o.Y == w.display.Min.Y {
			w.output = o
		}
	}
	if w.pointer, err = c.NewPointer(w.output); err != nil {
		return err
	}
	if w.keyboard == nil {
		if w.keyboard, err = c.NewKeyboard(w.xkb.keymap); err != nil {
			w.pointer = nil
			return err
		}
	}
	return nil
}

func (w *waylandDevice) move(p image.Point) {
	if w.ensure() == nil {
		w.pointer.Move(p.X-w.display.Min.X, p.Y-w.display.Min.Y)
	}
}

func (w *waylandDevice) button(b int, down bool) {
	codes := map[int]uint32{0: wayland.BtnLeft, 1: wayland.BtnMiddle, 2: wayland.BtnRight, 3: 0x113, 4: 0x114}
	if code, ok := codes[b]; ok && w.ensure() == nil {
		w.pointer.Button(code, down)
	}
}

func (w *waylandDevice) scroll(notches float64) {
	if w.ensure() == nil {
		w.pointer.Scroll(notches)
	}
}

func (w *waylandDevice) key(code uint32, down bool) {
	if w.ensure() != nil {
		return
	}
	dep, lat, lock, group := w.xkb.key(code, down)
	w.keyboard.Key(code, down)
	w.keyboard.Modifiers(dep, lat, lock, group)
}

func (w *waylandDevice) bounds() image.Rectangle {
	if w.ensure() != nil {
		return image.Rectangle{}
	}
	o := w.output
	w.display = image.Rect(o.X, o.Y, o.X+o.W, o.Y+o.H)
	return w.display
}

func (w *waylandDevice) setDisplay(display image.Rectangle) {
	w.display = display
	if w.pointer != nil {
		w.pointer.Close()
		w.pointer = nil
	}
	w.ensure()
}

// x11Device uses the XTEST extension of an X11 session.
type x11Device struct {
	conn *xgb.Conn
	root xproto.Window
}

func newX11Device() (*x11Device, error) {
	c, err := xgb.NewConn()
	if err != nil {
		return nil, err
	}
	if err := xtest.Init(c); err != nil {
		c.Close()
		return nil, errors.New("the X server has no XTEST extension")
	}
	return &x11Device{conn: c, root: xproto.Setup(c).DefaultScreen(c).Root}, nil
}

const (
	xKeyPress      = 2
	xKeyRelease    = 3
	xButtonPress   = 4
	xButtonRelease = 5
	xMotionNotify  = 6
)

func (x *x11Device) fake(typ, detail byte, rootX, rootY int16) {
	xtest.FakeInput(x.conn, typ, detail, 0, x.root, rootX, rootY, 0)
}

func (x *x11Device) move(p image.Point) { x.fake(xMotionNotify, 0, int16(p.X), int16(p.Y)) }

func (x *x11Device) button(b int, down bool) {
	buttons := map[int]byte{0: 1, 1: 2, 2: 3, 3: 8, 4: 9}
	if btn, ok := buttons[b]; ok {
		x.fake(map[bool]byte{true: xButtonPress, false: xButtonRelease}[down], btn, 0, 0)
	}
}

func (x *x11Device) scroll(notches float64) {
	btn := byte(5) // down
	if notches < 0 {
		btn = 4
	}
	for range max(1, int(math.Abs(notches))) {
		x.fake(xButtonPress, btn, 0, 0)
		x.fake(xButtonRelease, btn, 0, 0)
	}
}

func (x *x11Device) key(code uint32, down bool) {
	// X keycodes are evdev codes plus 8 on every current X server.
	x.fake(map[bool]byte{true: xKeyPress, false: xKeyRelease}[down], byte(code+8), 0, 0)
	x.conn.Sync()
}

func (x *x11Device) setDisplay(image.Rectangle) {}

func (x *x11Device) bounds() image.Rectangle {
	s := xproto.Setup(x.conn).DefaultScreen(x.conn)
	return image.Rect(0, 0, int(s.WidthInPixels), int(s.HeightInPixels))
}

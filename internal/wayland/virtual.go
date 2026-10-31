//go:build linux

package wayland

import (
	"errors"
	"time"

	"golang.org/x/sys/unix"
)

// Pointer is a virtual mouse (wlr-virtual-pointer).
type Pointer struct {
	c      *Conn
	id     uint32
	bound  bool // positions are relative to one output
	layout Output
}

// NewPointer creates a virtual mouse whose absolute positions cover the
// output.
func (c *Conn) NewPointer(o Output) (*Pointer, error) {
	if c.pointers.id == 0 {
		return nil, errors.New("the compositor does not offer a virtual mouse (wlr-virtual-pointer)")
	}
	p := &Pointer{c: c, id: c.newID(nil), layout: o}
	if c.pointers.Version >= 2 {
		p.bound = true
		return p, c.send(c.pointers.id, 2, c.seat, o.id, p.id) // create_virtual_pointer_with_output
	}
	return p, c.send(c.pointers.id, 0, c.seat, p.id) // create_virtual_pointer
}

// NewLayoutPointer creates a virtual mouse whose absolute positions cover
// every output, with x, y measured from the top left of the layout.
func (c *Conn) NewLayoutPointer() (*Pointer, error) {
	if c.pointers.id == 0 {
		return nil, errors.New("the compositor does not offer a virtual mouse (wlr-virtual-pointer)")
	}
	b := c.layoutBox()
	p := &Pointer{c: c, id: c.newID(nil), layout: Output{X: b.x, Y: b.y, W: b.w, H: b.h}}
	return p, c.send(c.pointers.id, 0, c.seat, p.id) // create_virtual_pointer
}

// Close removes the mouse.
func (p *Pointer) Close() { p.c.send(p.id, 8) }

// Move puts the pointer at pixel x, y of the output.
func (p *Pointer) Move(x, y int) {
	if p.bound {
		p.c.send(p.id, 1, now(), uint32(x), uint32(y), uint32(p.layout.W), uint32(p.layout.H))
	} else {
		// Unbound pointers span the whole layout; place it within the
		// bounding box of all outputs.
		box := p.c.layoutBox()
		p.c.send(p.id, 1, now(), uint32(p.layout.X-box.x+x), uint32(p.layout.Y-box.y+y), uint32(box.w), uint32(box.h))
	}
	p.c.send(p.id, 4) // frame
}

// Linux input button codes.
const (
	BtnLeft   = 0x110
	BtnRight  = 0x111
	BtnMiddle = 0x112
)

// Button presses or releases a button.
func (p *Pointer) Button(button uint32, down bool) {
	state := uint32(0)
	if down {
		state = 1
	}
	p.c.send(p.id, 2, now(), button, state)
	p.c.send(p.id, 4)
}

// Scroll turns the vertical wheel by notches, down positive.
func (p *Pointer) Scroll(notches float64) {
	t := now()
	p.c.send(p.id, 5, uint32(0)) // axis_source: wheel
	if n := int32(notches); float64(n) == notches && n != 0 {
		p.c.send(p.id, 7, t, uint32(0), fixed(notches*15), n) // axis_discrete
	} else {
		p.c.send(p.id, 3, t, uint32(0), fixed(notches*15)) // axis
	}
	p.c.send(p.id, 4)
}

type box struct{ x, y, w, h int }

func (c *Conn) layoutBox() box {
	outs := c.Outputs()
	if len(outs) == 0 {
		return box{w: 1, h: 1}
	}
	x0, y0, x1, y1 := outs[0].X, outs[0].Y, outs[0].X+outs[0].W, outs[0].Y+outs[0].H
	for _, o := range outs[1:] {
		x0, y0 = min(x0, o.X), min(y0, o.Y)
		x1, y1 = max(x1, o.X+o.W), max(y1, o.Y+o.H)
	}
	return box{x0, y0, x1 - x0, y1 - y0}
}

// Keyboard is a virtual keyboard (zwp-virtual-keyboard).
type Keyboard struct {
	c  *Conn
	id uint32
}

// NewKeyboard creates a virtual keyboard with an XKB keymap in text form.
func (c *Conn) NewKeyboard(keymap string) (*Keyboard, error) {
	if c.keyboard.id == 0 {
		return nil, errors.New("the compositor does not offer a virtual keyboard")
	}
	if c.seat == 0 {
		return nil, errors.New("the compositor has no seat")
	}
	k := &Keyboard{c: c, id: c.newID(nil)}
	if err := c.send(c.keyboard.id, 0, c.seat, k.id); err != nil {
		return nil, err
	}
	memfd, err := unix.MemfdCreate("remoter-keymap", unix.MFD_CLOEXEC)
	if err != nil {
		return nil, err
	}
	defer unix.Close(memfd)
	data := append([]byte(keymap), 0)
	if _, err := unix.Write(memfd, data); err != nil {
		return nil, err
	}
	return k, c.send(k.id, 0, uint32(1), fd(memfd), uint32(len(data))) // keymap, XKB v1
}

// Close removes the keyboard.
func (k *Keyboard) Close() { k.c.send(k.id, 3) }

// Key presses or releases a key by its Linux (evdev) code.
func (k *Keyboard) Key(code uint32, down bool) {
	state := uint32(0)
	if down {
		state = 1
	}
	k.c.send(k.id, 1, now(), code, state)
}

// Modifiers sets the modifier state, as XKB serialises it.
func (k *Keyboard) Modifiers(depressed, latched, locked, group uint32) {
	k.c.send(k.id, 2, depressed, latched, locked, group)
}

func now() uint32 { return uint32(time.Now().UnixMilli()) }

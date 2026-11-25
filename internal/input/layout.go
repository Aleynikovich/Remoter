package input

import "image"

// Layout is an injector for a keyboard-and-mouse sender (the merged
// desktop): its pointer positions are 0..1 of the box around every
// monitor, whatever display viewers watch.
type Layout interface {
	Handler
	// Screens are the monitors, in the coordinates the pointer moves in.
	Screens() []image.Rectangle
}

// NewLayout returns the layout injector for this platform. screens lists
// the monitors as screen capture sees them, for platforms whose input
// uses the same coordinates.
func NewLayout(screens func() []image.Rectangle) (Layout, error) {
	return newLayout(screens)
}

// boxLayout is a native injector that follows the box around screens.
type boxLayout struct {
	Handler
	screens func() []image.Rectangle
	box     image.Rectangle
}

func newBoxLayout(screens func() []image.Rectangle) (Layout, error) {
	box := union(screens())
	h, err := newNative(box)
	if err != nil {
		return nil, err
	}
	return &boxLayout{Handler: h, screens: screens, box: box}, nil
}

// Screens can change (a monitor plugged in); a sender asks for them when
// it joins, so the box follows then.
func (b *boxLayout) Screens() []image.Rectangle {
	rs := b.screens()
	if box := union(rs); box != b.box && !box.Empty() {
		b.box = box
		b.Handler.SetDisplay(box)
	}
	return rs
}

// SetDisplay is ignored: the layout always covers every monitor.
func (b *boxLayout) SetDisplay(image.Rectangle) {}

func union(rs []image.Rectangle) image.Rectangle {
	var u image.Rectangle
	for _, r := range rs {
		u = u.Union(r)
	}
	return u
}

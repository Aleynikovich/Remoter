// Package protocol defines the control messages exchanged between the relay
// server, host agents and viewers.
//
// Control messages are JSON text frames. Screen updates from a host are binary
// WebSocket messages; the relay forwards them unchanged to every authenticated
// viewer. Their layout is described in package stream. An update only carries
// the parts of the screen that changed, so a viewer asks for a full picture
// with TypeRefresh when it joins, and acknowledges every update with TypeAck so
// the host never runs more than a few updates ahead of the slowest viewer.
package protocol

import "image"

// Message types.
const (
	// host -> server: ask for a session ID. ID, if set, is the one the host
	// would like to keep (it had it before a restart); the server uses it if
	// it is free.
	TypeRegister = "register"
	// server -> host: the assigned session ID.
	TypeRegistered = "registered"
	// viewer -> server: request access to a host. An empty ID means the only
	// host on the server.
	TypeConnect = "connect"
	// server -> host: a viewer wants access; the host checks the password.
	TypeAuth = "auth"
	// host -> server: the host's verdict on an auth request.
	TypeAuthResult = "auth_result"
	// server -> viewer: access granted, frames will follow.
	TypeConnected = "connected"
	// viewer -> server -> host: a mouse or keyboard event.
	TypeInput = "input"
	// viewer -> server -> host: send the whole screen in the next update.
	TypeRefresh = "refresh"
	// viewer -> server -> host: the update with this Seq has been drawn.
	TypeAck = "ack"
	// viewer -> server -> host, or host -> server -> every viewer: the
	// sender's clipboard now holds Text.
	TypeClipboard = "clipboard"
	// host -> server -> every viewer: the mouse pointer now looks like
	// Cursor, a CSS cursor name.
	TypeCursor = "cursor"
	// host -> server -> every viewer: the host has Count displays and is
	// showing Display (0-based). With All set the host can also show every
	// display at once, as Display Count; a viewer selects that the same way.
	// Sizes holds each display's size in pixels (and every display's
	// together, last, with All), so viewers can split a display into
	// virtual monitors; Region is the part of Display being shown, if not
	// all of it.
	TypeDisplays = "displays"
	// host -> server -> every viewer: why the host sends JPEG instead of
	// H.264 video, in Text; empty when it can send video.
	TypeVideo = "video"
	// viewer -> server -> host: use quality Text ("auto", "sharp",
	// "balanced" or "smooth"). host -> server -> every viewer: the quality
	// in use and the Scale (percent of full resolution) video is sent at.
	TypeQuality = "quality"
	// viewer -> server -> host: turn data saver on (OK set) or off for
	// this viewer, for a metered link. host -> server -> every viewer:
	// whether the stream is in data saver mode (OK), which it is while any
	// viewer asks for it.
	TypeDataSaver = "data_saver"
	// viewer -> server -> host: draw the host's real mouse pointer into
	// the picture (OK set) or not, for this viewer. host -> server -> every
	// viewer: whether it is drawn (OK), which it is while any viewer asks.
	TypeRealPointer = "real_pointer"
	// viewer -> server -> host: send diagnostics. host -> server -> every
	// viewer: the host's diagnostics (settings, recent log) in Text.
	TypeDiagnostics = "diagnostics"
	// viewer -> server -> host: show Display instead, or only its Region
	// (a virtual monitor) when that is set.
	TypeSelectDisplay = "select_display"
	// viewer -> server -> host: a keyboard-and-mouse sender ("remoter kvm",
	// the merged desktop). Text "join" makes this viewer a sender: it gets
	// no picture, its pointer positions are 0..1 of the box around every
	// monitor (whatever display other viewers watch), and clipboard
	// changes reach it as they would a watching viewer. Text "enter" and
	// "leave" say the pointer crossed onto or off this computer; on leave
	// the host lets go of every key and button the sender holds.
	// host -> server -> every viewer: Screens, every monitor's rectangle in
	// the host's layout, in reply to a join. Other viewers ignore it.
	TypeKVM = "kvm"
	// server -> host: a viewer went away.
	TypeViewerLeft = "viewer_left"
	// server -> any: something went wrong; the connection is usually closed next.
	TypeError = "error"
)

// Message is the envelope for every control message. Unused fields are omitted.
type Message struct {
	Type     string  `json:"type"`
	ID       string  `json:"id,omitempty"`
	Password string  `json:"password,omitempty"`
	Viewer   string  `json:"viewer,omitempty"`
	OK       bool    `json:"ok,omitempty"`
	Error    string  `json:"error,omitempty"`
	Input    *Input  `json:"input,omitempty"`
	Seq      uint32  `json:"seq,omitempty"`
	Text     string  `json:"text,omitempty"`
	Cursor   string  `json:"cursor,omitempty"`
	Display  int     `json:"display,omitempty"`
	Count    int     `json:"count,omitempty"`
	Scale    int     `json:"scale,omitempty"`
	All      bool    `json:"all,omitempty"`
	Sizes    []Size  `json:"sizes,omitempty"`
	Region   *Region `json:"region,omitempty"`
	Screens  []Rect  `json:"screens,omitempty"`
	// Video is set on TypeRefresh when the viewer can decode H.264.
	Video bool `json:"video,omitempty"`
}

// FromViewer reports whether the relay should pass a viewer message of this
// type on to the host.
func FromViewer(typ string) bool {
	switch typ {
	case TypeInput, TypeRefresh, TypeAck, TypeClipboard, TypeSelectDisplay, TypeDiagnostics, TypeQuality, TypeDataSaver, TypeRealPointer, TypeKVM:
		return true
	}
	return false
}

// ToViewers reports whether the relay should pass a host message of this
// type on to every viewer.
func ToViewers(typ string) bool {
	switch typ {
	case TypeClipboard, TypeCursor, TypeDisplays, TypeVideo, TypeDiagnostics, TypeQuality, TypeDataSaver, TypeRealPointer, TypeKVM:
		return true
	}
	return false
}

// Input is a remote control event. Coordinates are normalised to 0..1 of the
// host screen so the viewer does not need to know the host resolution.
type Input struct {
	Kind   string  `json:"kind"` // mousemove, mousedown, mouseup, wheel, keydown, keyup
	X      float64 `json:"x,omitempty"`
	Y      float64 `json:"y,omitempty"`
	Button int     `json:"button,omitempty"`
	DeltaY float64 `json:"deltaY,omitempty"`
	// DeltaX is the horizontal wheel, right positive, like DeltaY in
	// about 100 per notch.
	DeltaX float64 `json:"deltaX,omitempty"`
	Key    string  `json:"key,omitempty"`
	Code   string  `json:"code,omitempty"`
}

// Size is a width and height in pixels.
type Size struct {
	W int `json:"w"`
	H int `json:"h"`
}

// Rect is a rectangle in pixels: a monitor in a host's layout.
type Rect struct {
	X int `json:"x"`
	Y int `json:"y"`
	W int `json:"w"`
	H int `json:"h"`
}

// Region is a part of a display, as fractions 0..1 of its width and
// height: a virtual monitor. Input coordinates and the picture then cover
// only the region.
type Region struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	W float64 `json:"w"`
	H float64 `json:"h"`
}

// Clean returns r clipped to the display, or nil when it is (nearly) the
// whole display or makes no sense, which both mean the whole display.
func (r *Region) Clean() *Region {
	if r == nil {
		return nil
	}
	c := func(v float64) float64 { return min(max(v, 0), 1) }
	x0, y0 := c(r.X), c(r.Y)
	x1, y1 := c(r.X+r.W), c(r.Y+r.H)
	if x1-x0 < 0.01 || y1-y0 < 0.01 || x0 < 0.001 && y0 < 0.001 && x1 > 0.999 && y1 > 0.999 {
		return nil
	}
	return &Region{X: x0, Y: y0, W: x1 - x0, H: y1 - y0}
}

// Of returns the part of rect r covers. A nil region is all of rect.
func (r *Region) Of(rect image.Rectangle) image.Rectangle {
	if r == nil || rect.Empty() {
		return rect
	}
	w, h := float64(rect.Dx()), float64(rect.Dy())
	x0 := rect.Min.X + int(r.X*w+0.5)
	y0 := rect.Min.Y + int(r.Y*h+0.5)
	x1 := rect.Min.X + int((r.X+r.W)*w+0.5)
	y1 := rect.Min.Y + int((r.Y+r.H)*h+0.5)
	return image.Rect(x0, y0, x1, y1).Intersect(rect)
}

// Map turns a point given as 0..1 of the region into 0..1 of the whole
// display. A nil region maps every point to itself.
func (r *Region) Map(x, y float64) (float64, float64) {
	if r == nil {
		return x, y
	}
	c := func(v float64) float64 { return min(max(v, 0), 1) }
	return r.X + c(x)*r.W, r.Y + c(y)*r.H
}

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

// Message types.
const (
	// host -> server: ask for a session ID.
	TypeRegister = "register"
	// server -> host: the assigned session ID.
	TypeRegistered = "registered"
	// viewer -> server: request access to a host.
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
	// server -> host: a viewer went away.
	TypeViewerLeft = "viewer_left"
	// server -> any: something went wrong; the connection is usually closed next.
	TypeError = "error"
)

// Message is the envelope for every control message. Unused fields are omitted.
type Message struct {
	Type     string `json:"type"`
	ID       string `json:"id,omitempty"`
	Password string `json:"password,omitempty"`
	Viewer   string `json:"viewer,omitempty"`
	OK       bool   `json:"ok,omitempty"`
	Error    string `json:"error,omitempty"`
	Input    *Input `json:"input,omitempty"`
	Seq      uint32 `json:"seq,omitempty"`
}

// FromViewer reports whether the relay should pass a viewer message of this
// type on to the host.
func FromViewer(typ string) bool {
	switch typ {
	case TypeInput, TypeRefresh, TypeAck:
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
	Key    string  `json:"key,omitempty"`
	Code   string  `json:"code,omitempty"`
}

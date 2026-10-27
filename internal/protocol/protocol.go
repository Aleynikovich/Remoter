// Package protocol defines the control messages exchanged between the relay
// server, host agents and viewers.
//
// Control messages are JSON text frames. Screen frames from a host are binary
// WebSocket messages holding one JPEG image each; the relay forwards them
// unchanged to every authenticated viewer.
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

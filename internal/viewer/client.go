// Package viewer holds the platform independent half of the desktop viewer:
// the connection to the relay, the screen update formats, key and pointer
// mapping and saved settings. The window itself lives in cmd/remoter-viewer.
package viewer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/aleynikovich/remoter/internal/protocol"
)

// EventKind says what an Event carries.
type EventKind int

const (
	// EventConnected: the host accepted the password; Text is the session ID.
	EventConnected EventKind = iota
	// EventFrame: a binary screen update in Data.
	EventFrame
	// EventMessage: a control message from the host in Msg (cursor,
	// displays, clipboard).
	EventMessage
	// EventStatus: Text describes a passing state, like reconnecting.
	EventStatus
	// EventEnded: the session is over; Text says why.
	EventEnded
)

// Event is something the window has to react to.
type Event struct {
	Kind EventKind
	Text string
	Data []byte
	Msg  protocol.Message
}

// Reconnect is how long a session that dropped (the host updating itself, a
// network blip) keeps being retried before the viewer gives up.
const Reconnect = 60 * time.Second

// Client is a viewer connection to one host through a relay.
type Client struct {
	URL      string // relay viewer endpoint, see ViewerURL
	ID       string // empty for the only host on the relay
	Password string
	// Wake is called after every event is queued, from the network
	// goroutine, so a window waiting for input events can look at Events.
	Wake func()

	events chan Event
	out    chan []byte
}

// NewClient returns a client for the relay at addr (host:port or a URL).
func NewClient(addr, id, password string) (*Client, error) {
	u, err := ViewerURL(addr)
	if err != nil {
		return nil, err
	}
	return &Client{
		URL:      u,
		ID:       strings.Join(strings.Fields(id), ""),
		Password: password,
		events:   make(chan Event, 256),
		out:      make(chan []byte, 4096),
	}, nil
}

// ViewerURL turns what a person types ("100.1.2.3:8080", "http://relay",
// "wss://relay/ws/viewer") into the relay's viewer WebSocket URL.
func ViewerURL(addr string) (string, error) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return "", errors.New("enter the server address")
	}
	if !strings.Contains(addr, "://") {
		addr = "ws://" + addr
	}
	u, err := url.Parse(addr)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("bad server address %q", addr)
	}
	switch u.Scheme {
	case "http", "ws":
		u.Scheme = "ws"
	case "https", "wss":
		u.Scheme = "wss"
	default:
		return "", fmt.Errorf("bad server address %q", addr)
	}
	if u.Path == "" || u.Path == "/" {
		u.Path = "/ws/viewer"
	}
	return u.String(), nil
}

// HTTPBase is the relay's web address for a viewer WebSocket URL:
// ws://100.1.2.3:8080/ws/viewer gives http://100.1.2.3:8080.
func HTTPBase(viewerURL string) string {
	u, err := url.Parse(viewerURL)
	if err != nil {
		return ""
	}
	scheme := "http"
	if u.Scheme == "wss" {
		scheme = "https"
	}
	return scheme + "://" + u.Host
}

// Events delivers what happened on the connection, in order.
func (c *Client) Events() <-chan Event { return c.events }

// Send queues a control message for the host. Mouse moves are dropped rather
// than queued when the link is badly behind; everything else waits.
func (c *Client) Send(m any) {
	data, err := json.Marshal(m)
	if err != nil {
		return
	}
	if msg, ok := m.(protocol.Message); ok && msg.Input != nil && msg.Input.Kind == "mousemove" {
		select {
		case c.out <- data:
		default:
		}
		return
	}
	c.out <- data
}

// Input sends a mouse or keyboard event.
func (c *Client) Input(in protocol.Input) {
	c.Send(protocol.Message{Type: protocol.TypeInput, Input: &in})
}

// Ack tells the host the update with seq has been drawn (or dropped).
func (c *Client) Ack(seq uint32) {
	c.Send(protocol.Message{Type: protocol.TypeAck, Seq: seq})
}

// Refresh asks for a complete picture, as H.264 when video is set.
func (c *Client) Refresh(video bool) {
	c.Send(protocol.Message{Type: protocol.TypeRefresh, Video: video})
}

func (c *Client) emit(e Event) {
	c.events <- e
	if c.Wake != nil {
		c.Wake()
	}
}

// Run connects and keeps the session open until ctx ends, the host refuses
// it, or the connection stays down for longer than Reconnect. It always
// finishes with an EventEnded.
func (c *Client) Run(ctx context.Context) {
	var lostAt time.Time
	everConnected := false
	for {
		connected, reason := c.session(ctx)
		everConnected = everConnected || connected
		if ctx.Err() != nil {
			c.emit(Event{Kind: EventEnded, Text: "Disconnected."})
			return
		}
		if connected {
			lostAt = time.Now()
		}
		if !everConnected || reason.refused || time.Since(lostAt) > Reconnect {
			text := reason.text
			if text == "" && everConnected {
				text = "Session ended."
			} else if text == "" {
				text = "Could not reach the server."
			}
			c.emit(Event{Kind: EventEnded, Text: text})
			return
		}
		c.emit(Event{Kind: EventStatus, Text: "Connection lost, reconnecting…"})
		select {
		case <-ctx.Done():
		case <-time.After(time.Second):
		}
	}
}

type endReason struct {
	text    string
	refused bool // the host said no; retrying will not help
}

// session runs one connection and reports whether the host let us in.
func (c *Client) session(ctx context.Context) (bool, endReason) {
	var connected atomic.Bool
	var reason endReason
	dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	conn, _, err := websocket.Dial(dialCtx, c.URL, nil)
	cancel()
	if err != nil {
		return false, endReason{text: "Could not reach the server."}
	}
	defer conn.CloseNow()
	// A full screen of tiles at high quality is a few megabytes.
	conn.SetReadLimit(64 << 20)

	ctx, stop := context.WithCancel(ctx)
	defer stop()
	hello, _ := json.Marshal(protocol.Message{Type: protocol.TypeConnect, ID: c.ID, Password: c.Password})
	if err := conn.Write(ctx, websocket.MessageText, hello); err != nil {
		return false, endReason{}
	}
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case data := <-c.out:
				if !connected.Load() {
					// Left over from the previous connection.
					continue
				}
				if err := conn.Write(ctx, websocket.MessageText, data); err != nil {
					conn.CloseNow()
					return
				}
			}
		}
	}()

	for {
		typ, data, err := conn.Read(ctx)
		if err != nil {
			return connected.Load(), reason
		}
		if typ == websocket.MessageBinary {
			if connected.Load() {
				c.emit(Event{Kind: EventFrame, Data: data})
			}
			continue
		}
		var m protocol.Message
		if json.Unmarshal(data, &m) != nil {
			continue
		}
		switch m.Type {
		case protocol.TypeConnected:
			connected.Store(true)
			c.emit(Event{Kind: EventConnected, Text: m.ID})
		case protocol.TypeError:
			reason = endReason{text: m.Error, refused: !connected.Load()}
		default:
			c.emit(Event{Kind: EventMessage, Msg: m})
		}
	}
}

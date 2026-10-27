// Package host implements the host agent: it registers with the relay, checks
// viewer passwords, streams the screen and receives input events.
package host

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"image/jpeg"
	"log"
	"math/big"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/aleynikovich/remoter/internal/capture"
	"github.com/aleynikovich/remoter/internal/protocol"
)

// InputHandler applies remote input events on the host.
type InputHandler interface {
	HandleInput(viewer string, in protocol.Input)
}

// releaser is implemented by input handlers that track held keys and buttons.
type releaser interface {
	ReleaseAll(viewer string)
}

// LogInput logs input events instead of applying them.
type LogInput struct{}

func (LogInput) HandleInput(viewer string, in protocol.Input) {
	log.Printf("input from viewer %s: %+v", viewer, in)
}

// Agent is a host session.
type Agent struct {
	ServerURL string // e.g. ws://localhost:8080/ws/host
	Password  string
	Source    capture.Source
	Input     InputHandler
	FPS       int
	Quality   int // JPEG quality, 1..100

	// OnRegistered is called with the session ID once the relay assigns it.
	OnRegistered func(id string)

	viewers atomic.Int32
}

// RandomPassword returns a short password that is easy to read out loud.
func RandomPassword() string {
	const alphabet = "abcdefghjkmnpqrstuvwxyz23456789"
	b := make([]byte, 6)
	for i := range b {
		n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		b[i] = alphabet[n.Int64()]
	}
	return string(b)
}

// Run connects to the relay and serves until ctx ends or the connection drops.
func (a *Agent) Run(ctx context.Context) error {
	if a.Password == "" {
		return errors.New("password must not be empty")
	}
	if a.FPS <= 0 {
		a.FPS = 10
	}
	if a.Quality <= 0 {
		a.Quality = 70
	}
	if a.Input == nil {
		a.Input = LogInput{}
	}

	conn, _, err := websocket.Dial(ctx, a.ServerURL, nil)
	if err != nil {
		return fmt.Errorf("dial relay: %w", err)
	}
	defer conn.CloseNow()
	conn.SetReadLimit(1 << 16)

	if err := send(ctx, conn, protocol.Message{Type: protocol.TypeRegister}); err != nil {
		return err
	}
	_, data, err := conn.Read(ctx)
	if err != nil {
		return err
	}
	var reg protocol.Message
	if err := json.Unmarshal(data, &reg); err != nil || reg.Type != protocol.TypeRegistered {
		return fmt.Errorf("unexpected reply to register: %s", data)
	}
	if a.OnRegistered != nil {
		a.OnRegistered(reg.ID)
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	errc := make(chan error, 1)
	go func() {
		errc <- a.stream(ctx, conn)
		cancel()
	}()

	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			select {
			case serr := <-errc:
				return fmt.Errorf("stream: %w", serr)
			default:
				return err
			}
		}
		var m protocol.Message
		if json.Unmarshal(data, &m) != nil {
			continue
		}
		switch m.Type {
		case protocol.TypeAuth:
			ok := subtle.ConstantTimeCompare([]byte(m.Password), []byte(a.Password)) == 1
			if ok {
				a.viewers.Add(1)
				log.Printf("viewer %s connected", m.Viewer)
			} else {
				log.Printf("viewer %s used a wrong password", m.Viewer)
			}
			if err := send(ctx, conn, protocol.Message{Type: protocol.TypeAuthResult, Viewer: m.Viewer, OK: ok}); err != nil {
				return err
			}
		case protocol.TypeViewerLeft:
			a.viewers.Add(-1)
			if r, ok := a.Input.(releaser); ok {
				r.ReleaseAll(m.Viewer)
			}
			log.Printf("viewer %s left", m.Viewer)
		case protocol.TypeInput:
			if m.Input != nil {
				a.Input.HandleInput(m.Viewer, *m.Input)
			}
		}
	}
}

// stream sends JPEG frames while at least one viewer is connected.
func (a *Agent) stream(ctx context.Context, conn *websocket.Conn) error {
	t := time.NewTicker(time.Second / time.Duration(a.FPS))
	defer t.Stop()
	var buf bytes.Buffer
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
		if a.viewers.Load() <= 0 {
			continue
		}
		img, err := a.Source.Capture()
		if err != nil {
			log.Printf("capture: %v", err)
			continue
		}
		buf.Reset()
		if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: a.Quality}); err != nil {
			return err
		}
		if err := conn.Write(ctx, websocket.MessageBinary, buf.Bytes()); err != nil {
			return err
		}
	}
}

func send(ctx context.Context, c *websocket.Conn, m protocol.Message) error {
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return c.Write(ctx, websocket.MessageText, data)
}

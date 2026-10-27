// Package host implements the host agent: it registers with the relay, checks
// viewer passwords, streams the screen and receives input events.
package host

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/big"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/aleynikovich/remoter/internal/capture"
	"github.com/aleynikovich/remoter/internal/protocol"
	"github.com/aleynikovich/remoter/internal/stream"
)

const (
	// window is how many updates may be on their way to a viewer before the
	// host waits for that viewer's ack. Waiting instead of queueing keeps the
	// picture current rather than seconds behind on a slow link.
	window = 3
	// staleAfter is how long a viewer may go without acking before the host
	// stops waiting for it. The relay drops it once its queue overflows.
	staleAfter = 5 * time.Second
	// refineAfter is how long a tile must stay still before it is resent
	// at high quality.
	refineAfter = 300 * time.Millisecond
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
	FPS       int // upper bound; the actual rate adapts to the viewers
	Quality   int // JPEG quality for changing areas, 1..100
	// RefineQuality is the JPEG quality areas are resent at once they stop
	// changing. Zero means 90.
	RefineQuality int

	// OnRegistered is called with the session ID once the relay assigns it.
	OnRegistered func(id string)

	flow flow
}

// flow tracks which viewers are watching and how far behind each one is.
type flow struct {
	mu      sync.Mutex
	seq     uint32
	full    bool
	viewers map[string]*viewerState
	wake    chan struct{}
}

type viewerState struct {
	acked   uint32
	lastAck time.Time
}

func (f *flow) poke() {
	select {
	case f.wake <- struct{}{}:
	default:
	}
}

// join starts streaming to a viewer, beginning with a full update.
func (f *flow) join(viewer string) {
	f.mu.Lock()
	f.viewers[viewer] = &viewerState{acked: f.seq, lastAck: time.Now()}
	f.full = true
	f.mu.Unlock()
	f.poke()
}

func (f *flow) leave(viewer string) {
	f.mu.Lock()
	delete(f.viewers, viewer)
	f.mu.Unlock()
	f.poke()
}

func (f *flow) ack(viewer string, seq uint32) {
	f.mu.Lock()
	if v := f.viewers[viewer]; v != nil && seq > v.acked && seq <= f.seq {
		v.acked, v.lastAck = seq, time.Now()
	}
	f.mu.Unlock()
	f.poke()
}

// ready reports whether someone is watching and every live viewer has room
// for another update.
func (f *flow) ready() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.viewers) == 0 {
		return false
	}
	for _, v := range f.viewers {
		if f.seq-v.acked >= window && time.Since(v.lastAck) < staleAfter {
			return false
		}
	}
	return true
}

// next reserves a sequence number for an update and reports whether it must
// cover the whole screen.
func (f *flow) next() (seq uint32, full bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	full, f.full = f.full, false
	return f.seq + 1, full
}

func (f *flow) sent(seq uint32) {
	f.mu.Lock()
	f.seq = seq
	f.mu.Unlock()
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
		a.Quality = 60
	}
	if a.RefineQuality <= 0 {
		a.RefineQuality = 90
	}
	a.flow = flow{viewers: make(map[string]*viewerState), wake: make(chan struct{}, 1)}
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
				log.Printf("viewer %s connected", m.Viewer)
			} else {
				log.Printf("viewer %s used a wrong password", m.Viewer)
			}
			if err := send(ctx, conn, protocol.Message{Type: protocol.TypeAuthResult, Viewer: m.Viewer, OK: ok}); err != nil {
				return err
			}
		case protocol.TypeRefresh:
			a.flow.join(m.Viewer)
		case protocol.TypeAck:
			a.flow.ack(m.Viewer, m.Seq)
		case protocol.TypeViewerLeft:
			a.flow.leave(m.Viewer)
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

// stream captures the screen and sends what changed, at most FPS times a
// second and only as fast as the slowest viewer draws.
func (a *Agent) stream(ctx context.Context, conn *websocket.Conn) error {
	enc := &stream.Encoder{Quality: a.Quality, RefineQuality: a.RefineQuality, RefineAfter: refineAfter}
	interval := time.Second / time.Duration(a.FPS)
	var buf []byte
	next := time.Now()
	for {
		wait := time.Until(next)
		for wait > 0 || !a.flow.ready() {
			if wait <= 0 {
				// Waiting on viewers; still wake now and then so a still
				// screen gets refined and stale viewers are noticed.
				wait = interval
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-a.flow.wake:
			case <-time.After(wait):
			}
			wait = time.Until(next)
		}
		next = time.Now().Add(interval)

		img, err := a.Source.Capture()
		if err != nil {
			log.Printf("capture: %v", err)
			continue
		}
		seq, full := a.flow.next()
		u, err := enc.Encode(img, full, time.Now())
		if err != nil {
			return err
		}
		if u == nil {
			continue
		}
		u.Seq = seq
		buf = u.Marshal(buf)
		if err := conn.Write(ctx, websocket.MessageBinary, buf); err != nil {
			return err
		}
		a.flow.sent(seq)
	}
}

func send(ctx context.Context, c *websocket.Conn, m protocol.Message) error {
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return c.Write(ctx, websocket.MessageText, data)
}

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
	"image"
	"image/draw"
	"log"
	"math/big"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/aleynikovich/remoter/internal/capture"
	"github.com/aleynikovich/remoter/internal/clipboard"
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
	// videoSettle is how long video keeps encoding after the screen stops
	// changing, so the picture sharpens to full quality before going quiet.
	videoSettle = time.Second
	// clipboardPoll is how often the host clipboard is checked for changes.
	clipboardPoll = 250 * time.Millisecond
)

// InputHandler applies remote input events on the host.
type InputHandler interface {
	HandleInput(viewer string, in protocol.Input)
}

// releaser is implemented by input handlers that track held keys and buttons.
type releaser interface {
	ReleaseAll(viewer string)
}

// VideoEncoder compresses captures into H.264 frames (Annex B). Encode
// returns nil data when the encoder drops the frame.
type VideoEncoder interface {
	Size() (w, h int)
	Encode(img *image.RGBA, key bool) (data []byte, isKey bool, err error)
	Close()
}

// Monitors switches the shared display.
type Monitors interface {
	Count() int
	Current() int
	Select(display int) error
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

	// Clipboard, if set, is shared with viewers in both directions.
	Clipboard clipboard.Clipboard
	// Cursor, if set, returns the pointer shape as a CSS cursor name. The
	// viewer shows it on its own pointer, which moves without lag.
	Cursor func() string
	// Monitors, if set, lets viewers switch between displays.
	Monitors Monitors
	// NewVideo, if set, starts an H.264 encoder for w×h captures. It is
	// used while every viewer can decode H.264, and JPEG tiles otherwise.
	// An error turns video off for the rest of the session.
	NewVideo func(w, h int) (VideoEncoder, error)
	// VideoReady, if set, reports whether NewVideo can be called yet (the
	// codec may still be downloading).
	VideoReady func() bool

	// ID is the session ID to ask the relay for, so viewers can reconnect
	// to the same ID after a restart. Run sets it to the ID it got.
	ID string

	// OnRegistered is called with the session ID once the relay assigns it.
	OnRegistered func(id string)

	flow flow

	clipMu  sync.Mutex
	clipSeq uint32 // clipboard sequence number last sent or written
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
	video   bool
}

func (f *flow) poke() {
	select {
	case f.wake <- struct{}{}:
	default:
	}
}

// join starts streaming to a viewer, beginning with a full update.
func (f *flow) join(viewer string, video bool) {
	f.mu.Lock()
	f.viewers[viewer] = &viewerState{acked: f.seq, lastAck: time.Now(), video: video}
	f.full = true
	f.mu.Unlock()
	f.poke()
}

// refresh makes the next update cover the whole screen.
func (f *flow) refresh() {
	f.mu.Lock()
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

// allVideo reports whether every viewer can decode H.264.
func (f *flow) allVideo() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, v := range f.viewers {
		if !v.video {
			return false
		}
	}
	return len(f.viewers) > 0
}

// watched reports whether any viewer is streaming.
func (f *flow) watched() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.viewers) > 0
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
	conn.SetReadLimit(1 << 20)

	if err := send(ctx, conn, protocol.Message{Type: protocol.TypeRegister, ID: a.ID}); err != nil {
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
	a.ID = reg.ID
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
	if a.Clipboard != nil {
		a.clipSeq = a.Clipboard.Seq()
		go a.watchClipboard(ctx, conn)
	}

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
			a.flow.join(m.Viewer, m.Video)
			if err := a.sendDisplays(ctx, conn); err != nil {
				return err
			}
		case protocol.TypeSelectDisplay:
			if a.Monitors == nil {
				continue
			}
			if err := a.Monitors.Select(m.Display); err != nil {
				log.Printf("viewer %s: select display %d: %v", m.Viewer, m.Display, err)
				continue
			}
			a.flow.refresh()
			if err := a.sendDisplays(ctx, conn); err != nil {
				return err
			}
		case protocol.TypeAck:
			a.flow.ack(m.Viewer, m.Seq)
		case protocol.TypeClipboard:
			a.setClipboard(m.Text)
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
	st := &streamer{a: a, tiles: &stream.Encoder{Quality: a.Quality, RefineQuality: a.RefineQuality, RefineAfter: refineAfter}}
	defer st.closeVideo()
	interval := time.Second / time.Duration(a.FPS)
	var cursor string
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
		if a.Cursor != nil {
			if c := a.Cursor(); c != "" && (c != cursor || full) {
				cursor = c
				if err := send(ctx, conn, protocol.Message{Type: protocol.TypeCursor, Cursor: c}); err != nil {
					return err
				}
			}
		}
		msg, err := st.encode(img, seq, full)
		if err != nil {
			return err
		}
		if msg == nil {
			continue
		}
		if err := conn.Write(ctx, websocket.MessageBinary, msg); err != nil {
			return err
		}
		a.flow.sent(seq)
	}
}

// streamer picks between H.264 video and JPEG tiles for each capture.
type streamer struct {
	a     *Agent
	tiles *stream.Encoder
	buf   []byte

	video      VideoEncoder
	videoOff   bool // the encoder failed; stay on tiles
	usingVideo bool
	prev       []byte // last capture, to skip encoding a still screen
	changed    time.Time
}

func (s *streamer) encode(src image.Image, seq uint32, full bool) ([]byte, error) {
	img := toRGBA(src)
	wantVideo := s.a.NewVideo != nil && !s.videoOff &&
		(s.a.VideoReady == nil || s.a.VideoReady()) && s.a.flow.allVideo()
	if wantVideo != s.usingVideo {
		s.usingVideo = wantVideo
		full = true // the other format starts from nothing
		if !wantVideo {
			s.closeVideo()
		}
	}
	if !wantVideo {
		u, err := s.tiles.Encode(img, full, time.Now())
		if u == nil || err != nil {
			return nil, err
		}
		u.Seq = seq
		s.buf = u.Marshal(s.buf)
		return s.buf, nil
	}

	w, h := img.Rect.Dx(), img.Rect.Dy()
	if s.video != nil {
		if vw, vh := s.video.Size(); vw != w&^1 || vh != h&^1 {
			s.closeVideo()
		}
	}
	if s.video == nil {
		v, err := s.a.NewVideo(w, h)
		if err != nil {
			log.Printf("H.264 unavailable, sending JPEG: %v", err)
			s.videoOff = true
			return s.encode(img, seq, true)
		}
		s.video = v
		full = true
	}

	// Skip a still screen, but only after it has had time to sharpen.
	now := time.Now()
	if !bytesEqualRGBA(s.prev, img) {
		s.changed = now
		s.prev = copyRGBA(s.prev, img)
	} else if !full && now.Sub(s.changed) > videoSettle {
		return nil, nil
	}
	data, key, err := s.video.Encode(img, full)
	if err != nil {
		log.Printf("H.264 failed, sending JPEG: %v", err)
		s.videoOff = true
		s.closeVideo()
		return s.encode(img, seq, true)
	}
	if data == nil {
		if full {
			// A dropped key frame would leave new viewers blank.
			s.a.flow.refresh()
		}
		return nil, nil
	}
	vw, vh := s.video.Size()
	s.buf = stream.MarshalVideo(s.buf, seq, vw, vh, key, data)
	return s.buf, nil
}

func (s *streamer) closeVideo() {
	if s.video != nil {
		s.video.Close()
		s.video = nil
	}
	s.prev = nil
}

func toRGBA(src image.Image) *image.RGBA {
	if img, ok := src.(*image.RGBA); ok {
		return img
	}
	img := image.NewRGBA(src.Bounds())
	draw.Draw(img, img.Rect, src, src.Bounds().Min, draw.Src)
	return img
}

func bytesEqualRGBA(prev []byte, img *image.RGBA) bool {
	w := img.Rect.Dx() * 4
	if len(prev) != w*img.Rect.Dy() {
		return false
	}
	for y := range img.Rect.Dy() {
		if !bytes.Equal(prev[y*w:(y+1)*w], img.Pix[y*img.Stride:y*img.Stride+w]) {
			return false
		}
	}
	return true
}

func copyRGBA(dst []byte, img *image.RGBA) []byte {
	w := img.Rect.Dx() * 4
	if n := w * img.Rect.Dy(); cap(dst) >= n {
		dst = dst[:n]
	} else {
		dst = make([]byte, n)
	}
	for y := range img.Rect.Dy() {
		copy(dst[y*w:], img.Pix[y*img.Stride:y*img.Stride+w])
	}
	return dst
}

// sendDisplays tells viewers which displays they can switch between.
func (a *Agent) sendDisplays(ctx context.Context, conn *websocket.Conn) error {
	if a.Monitors == nil {
		return nil
	}
	return send(ctx, conn, protocol.Message{Type: protocol.TypeDisplays, Count: a.Monitors.Count(), Display: a.Monitors.Current()})
}

// watchClipboard sends the host clipboard to viewers whenever it changes.
func (a *Agent) watchClipboard(ctx context.Context, conn *websocket.Conn) {
	t := time.NewTicker(clipboardPoll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		a.clipMu.Lock()
		seq := a.Clipboard.Seq()
		changed := seq != a.clipSeq
		a.clipSeq = seq
		a.clipMu.Unlock()
		if !changed || !a.flow.watched() {
			continue
		}
		text, err := a.Clipboard.ReadText()
		if err != nil {
			log.Printf("read clipboard: %v", err)
			continue
		}
		if text == "" || len(text) > clipboard.MaxText {
			continue
		}
		if err := send(ctx, conn, protocol.Message{Type: protocol.TypeClipboard, Text: text}); err != nil {
			return
		}
	}
}

// setClipboard puts a viewer's clipboard text on the host clipboard.
func (a *Agent) setClipboard(text string) {
	if a.Clipboard == nil || text == "" || len(text) > clipboard.MaxText {
		return
	}
	a.clipMu.Lock()
	defer a.clipMu.Unlock()
	if err := a.Clipboard.WriteText(text); err != nil {
		log.Printf("write clipboard: %v", err)
		return
	}
	// Do not echo the text back to the viewers.
	a.clipSeq = a.Clipboard.Seq()
}

func send(ctx context.Context, c *websocket.Conn, m protocol.Message) error {
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return c.Write(ctx, websocket.MessageText, data)
}

// Package host implements the host agent: it registers with the relay, checks
// viewer passwords, streams the screen and receives input events.
package host

import (
	"bytes"
	"cmp"
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
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/aleynikovich/remoter/internal/capture"
	"github.com/aleynikovich/remoter/internal/clipboard"
	"github.com/aleynikovich/remoter/internal/protocol"
	"github.com/aleynikovich/remoter/internal/stream"
)

const (
	// window is how many updates may always be on their way to a viewer
	// before the host waits for that viewer's ack. More may be, up to
	// maxWindow, while their bytes fit twice what the link delivers in
	// its quickest round trip: on a long link (a phone network) that
	// keeps the frame rate up, while waiting instead of queueing on a
	// slow one keeps the picture current rather than seconds behind.
	window    = 5
	maxWindow = 120
	// minRTTFor is how long a round-trip time stays the quickest seen.
	minRTTFor = 10 * time.Second
	// recheck is how often the stream loop wakes while it waits for
	// viewers, so a still screen gets refined and stale viewers noticed.
	recheck = 50 * time.Millisecond
	// minInterval keeps a source that returns at once from spinning a core
	// when there is no FPS cap; it is well above any display's rate.
	minInterval = 2 * time.Millisecond
	// staleAfter is how long a viewer may go without acking before the host
	// stops waiting for it. The relay drops it once its queue overflows.
	staleAfter = 5 * time.Second
	// lagAfter is how long the host waits for one viewer while another has
	// room, so a slow phone does not freeze everyone else's picture.
	lagAfter  = 250 * time.Millisecond
	lagPeriod = 5 * time.Second
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
	FPS       int // upper bound, or 0 for none; the rate adapts to the encoder and viewers
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
	// VideoStatus, if set, says why NewVideo cannot be called yet (the
	// codec may still be downloading, or failed to load). Empty means ready.
	VideoStatus  func() string
	restartVideo atomic.Bool

	// Diagnostics, if set, returns the host's settings and recent log for
	// viewers that ask for them.
	Diagnostics func() string

	// ID is the session ID to ask the relay for, so viewers can reconnect
	// to the same ID after a restart. Run sets it to the ID it got.
	ID string

	// OnRegistered is called with the session ID once the relay assigns it.
	OnRegistered func(id string)

	flow flow

	res *quality // resolution video is encoded at

	videoMu  sync.Mutex
	videoErr string // why the encoder was turned off for the session

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
	sentLog [2 * maxWindow]sentUpdate // by seq % len
}

// sentUpdate is when an update went out and how big it was.
type sentUpdate struct {
	seq   uint32
	at    time.Time
	bytes int
}

type viewerState struct {
	acked   uint32
	lastAck time.Time
	video   bool
	full    time.Time // when its window filled up, zero while it has room
	// blocked adds up how long the viewer has held the stream back since
	// blockedFrom. Holding it back for lagAfter of a lagPeriod marks it
	// lagging: the host stops waiting for it, for at least lagPeriod and
	// until it has caught up.
	blocked     time.Duration
	blockedFrom time.Time
	lagging     bool
	laggingAt   time.Time
	loggedLag   time.Time // last time lagging was logged, to keep it quiet

	rtt, minRTT time.Duration // latest and quickest (in minRTTFor) round trips
	minRTTAt    time.Time
	rate        float64 // bytes per second the viewer acked, recent best
	rateFrom    time.Time
	rateBytes   int
}

// room reports whether another update may go to v now.
func (f *flow) room(v *viewerState) bool {
	behind := f.seq - v.acked
	if behind < window {
		return true
	}
	if behind >= maxWindow || v.rate == 0 || v.minRTT == 0 {
		return false
	}
	inFlight := 0
	for s := v.acked + 1; s != f.seq+1; s++ {
		if u := f.sentLog[s%uint32(len(f.sentLog))]; u.seq == s {
			inFlight += u.bytes
		}
	}
	return float64(inFlight) < 2*v.rate*v.minRTT.Seconds()
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
		now := time.Now()
		for s := v.acked + 1; s != seq+1; s++ {
			if u := f.sentLog[s%uint32(len(f.sentLog))]; u.seq == s {
				v.rateBytes += u.bytes
			}
		}
		if u := f.sentLog[seq%uint32(len(f.sentLog))]; u.seq == seq {
			v.rtt = now.Sub(u.at)
			if v.minRTT == 0 || v.rtt < v.minRTT || now.Sub(v.minRTTAt) > minRTTFor {
				v.minRTT, v.minRTTAt = max(v.rtt, time.Millisecond), now
			}
		}
		if v.rateFrom.IsZero() {
			v.rateFrom = now
		} else if el := now.Sub(v.rateFrom); el >= 250*time.Millisecond {
			v.rate = max(float64(v.rateBytes)/el.Seconds(), 0.8*v.rate)
			v.rateFrom, v.rateBytes = now, 0
		}
		v.acked, v.lastAck = seq, now
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

// ready reports whether someone is watching and every viewer that keeps up
// has room for another update. A viewer whose window stays full for
// lagAfter is marked lagging and not waited for until it has caught up, so
// a slow phone cannot set everyone's frame rate; the relay drops it if it
// falls too far behind, and it reconnects.
func (f *flow) ready() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := time.Now()
	room, waiting := false, false
	for id, v := range f.viewers {
		behind := f.seq - v.acked
		if now.Sub(v.lastAck) >= staleAfter {
			continue // not acking at all (backgrounded, gone)
		}
		if v.lagging {
			if behind > 1 || now.Sub(v.laggingAt) < lagPeriod {
				continue
			}
			v.lagging = false
		}
		if now.Sub(v.blockedFrom) > lagPeriod {
			v.blocked, v.blockedFrom = 0, now
		}
		if f.room(v) {
			if !v.full.IsZero() {
				v.blocked += now.Sub(v.full)
				v.full = time.Time{}
			}
			room = true
			continue
		}
		if v.full.IsZero() {
			v.full = now
		}
		if v.blocked+now.Sub(v.full) < lagAfter {
			waiting = true
			continue
		}
		v.lagging, v.laggingAt = true, now
		v.full, v.blocked, v.blockedFrom = time.Time{}, 0, now
		if now.Sub(v.loggedLag) > time.Minute {
			v.loggedLag = now
			log.Printf("viewer %s cannot keep up, no longer waiting for it", id)
		}
	}
	if !room && !waiting && len(f.viewers) > 0 {
		// Everyone is lagging or silent: keep a trickle going so they
		// can recover, at most one update per window per viewer.
		for _, v := range f.viewers {
			if f.room(v) {
				return true
			}
		}
	}
	return room && !waiting
}

// summary describes each viewer's state for the log.
func (f *flow) summary() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var parts []string
	for id, v := range f.viewers {
		state := "keeping up"
		switch {
		case time.Since(v.lastAck) >= staleAfter:
			state = "not acking"
		case v.lagging:
			state = "lagging"
		}
		parts = append(parts, fmt.Sprintf("viewer %s %s (%d behind, round trip %s, %.1f Mbit/s)",
			id, state, f.seq-v.acked, v.rtt.Round(time.Millisecond), v.rate*8/1e6))
	}
	sort.Strings(parts)
	return strings.Join(parts, ", ")
}

// next reserves a sequence number for an update and reports whether it must
// cover the whole screen.
func (f *flow) next() (seq uint32, full bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	full, f.full = f.full, false
	return f.seq + 1, full
}

func (f *flow) sent(seq uint32, bytes int) {
	f.mu.Lock()
	f.seq = seq
	f.sentLog[seq%uint32(len(f.sentLog))] = sentUpdate{seq, time.Now(), bytes}
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
	if a.Quality <= 0 {
		a.Quality = 60
	}
	if a.RefineQuality <= 0 {
		a.RefineQuality = 90
	}
	a.flow = flow{viewers: make(map[string]*viewerState), wake: make(chan struct{}, 1)}
	if a.res == nil {
		a.res = newQuality()
	}
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
			if err := send(ctx, conn, protocol.Message{Type: protocol.TypeVideo, Text: a.whyNoVideo()}); err != nil {
				return err
			}
			if err := a.sendQuality(ctx, conn); err != nil {
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
		case protocol.TypeDiagnostics:
			text := "video: " + cmp.Or(a.whyNoVideo(), "H.264 on")
			if a.Diagnostics != nil {
				text = a.Diagnostics() + "\n" + text
			}
			if err := send(ctx, conn, protocol.Message{Type: protocol.TypeDiagnostics, Text: text}); err != nil {
				return err
			}
		case protocol.TypeQuality:
			if a.res.set(m.Text) {
				log.Printf("viewer %s set quality %s", m.Viewer, m.Text)
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

// stream captures the screen and sends what changed, as fast as the encoder
// and the viewers keep up (and at most FPS times a second if that is set). Encoding and sending
// run in their own goroutine, so the next capture overlaps them.
func (a *Agent) stream(ctx context.Context, conn *websocket.Conn) error {
	ctx, cancel := context.WithCancel(ctx)
	frames := make(chan frame)
	done := make(chan error, 1)
	go func() { done <- a.encodeAndSend(ctx, conn, frames) }()
	defer func() {
		cancel()
		close(frames)
		<-done
	}()

	interval := minInterval
	if a.FPS > 0 {
		interval = max(interval, time.Second/time.Duration(a.FPS))
	}
	var cursor string
	next := time.Now()
	lastFrame := next
	for {
		waitStart := time.Now()
		wait := time.Until(next)
		for wait > 0 || !a.flow.ready() {
			if wait <= 0 {
				wait = recheck
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case err := <-done:
				done <- err
				return err
			case <-a.flow.wake:
			case <-time.After(wait):
			}
			wait = time.Until(next)
		}
		next = time.Now().Add(interval)

		t0 := time.Now()
		img, err := a.Source.Capture()
		if err == nil && img.Bounds().Empty() {
			// Seen right after connecting to an X server; it would turn
			// video off for good.
			err = errors.New("empty capture")
		}
		if err != nil {
			log.Printf("capture: %v", err)
			continue
		}
		f := frame{img: img, wait: t0.Sub(waitStart), capture: time.Since(t0)}
		a.res.waited(f.wait, time.Since(lastFrame), time.Now())
		lastFrame = time.Now()
		f.seq, f.full = a.flow.next()
		if a.Cursor != nil {
			if c := a.Cursor(); c != "" && (c != cursor || f.full) {
				cursor = c
				if err := send(ctx, conn, protocol.Message{Type: protocol.TypeCursor, Cursor: c}); err != nil {
					return err
				}
			}
		}
		// Hand the capture over once the previous one is encoded. A
		// Source may reuse an image after the next Capture call, which
		// is why this waits instead of capturing again.
		select {
		case frames <- f:
		case err := <-done:
			done <- err
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// frame is a capture on its way to the encoder.
type frame struct {
	img           image.Image
	seq           uint32
	full          bool
	wait, capture time.Duration
}

// encodeAndSend encodes the captures it is handed and sends them.
func (a *Agent) encodeAndSend(ctx context.Context, conn *websocket.Conn, frames <-chan frame) error {
	st := &streamer{a: a, tiles: &stream.Encoder{Quality: a.Quality, RefineQuality: a.RefineQuality, RefineAfter: refineAfter}}
	defer st.closeVideo()
	tm := timings{start: time.Now()}
	for f := range frames {
		select {
		case <-a.res.changed:
			mode, scale := a.res.current()
			log.Printf("quality %s: video at %d%% of full resolution", mode, scale)
			if err := a.sendQuality(ctx, conn); err != nil {
				return err
			}
		default:
		}
		_, scale := a.res.current()
		tm.report(st.usingVideo, scale, a.flow.summary)
		tm.size = f.img.Bounds().Size()
		tm.wait += f.wait
		tm.capture += f.capture
		t1 := time.Now()
		msg, err := st.encode(f.img, f.seq, f.full)
		t2 := time.Now()
		tm.encode += t2.Sub(t1)
		tm.loops++
		if err != nil {
			return err
		}
		if msg == nil {
			continue
		}
		if err := conn.Write(ctx, websocket.MessageBinary, msg); err != nil {
			return err
		}
		tm.send += time.Since(t2)
		tm.frames++
		tm.bytes += len(msg)
		a.flow.sent(f.seq, len(msg))
	}
	return nil
}

// timings adds up where the stream loop spends its time and logs it every
// timingsEvery while frames are going out, to tell a slow capture or encoder
// from a slow network or viewer (time spent waiting for acks).
type timings struct {
	start                       time.Time
	wait, capture, encode, send time.Duration
	loops, frames, bytes        int
	size                        image.Point
}

const timingsEvery = 10 * time.Second

func (t *timings) report(video bool, scale int, viewers func() string) {
	el := time.Since(t.start)
	if el < timingsEvery {
		return
	}
	if t.frames > 0 {
		mode := "JPEG"
		if video {
			mode = fmt.Sprintf("H.264 at %d%%", scale)
		}
		per := func(d time.Duration) string {
			return (d / time.Duration(max(t.loops, 1))).Round(100 * time.Microsecond).String()
		}
		log.Printf("stream %s %dx%d: %.1f fps, %.1f Mbit/s; per capture: capture %s, encode %s, send %s; waited %.0f%% of the time for viewers or the frame interval; %s",
			mode, t.size.X, t.size.Y, float64(t.frames)/el.Seconds(), float64(t.bytes)*8/1e6/el.Seconds(),
			per(t.capture), per(t.encode), per(t.send), min(100, 100*t.wait.Seconds()/el.Seconds()), viewers())
	}
	*t = timings{start: time.Now()}
}

// streamer picks between H.264 video and JPEG tiles for each capture.
type streamer struct {
	a     *Agent
	tiles *stream.Encoder
	buf   []byte

	video      VideoEncoder
	usingVideo bool
	scaled     *image.RGBA // the capture shrunk to the quality's scale
	prev       []byte      // last capture, to skip encoding a still screen
	changed    time.Time
}

// RestartVideo makes the stream open a new video encoder from NewVideo for
// its next picture, as when a better encoder became available.
func (a *Agent) RestartVideo() { a.restartVideo.Store(true) }

func (s *streamer) encode(src image.Image, seq uint32, full bool) ([]byte, error) {
	if s.a.restartVideo.Swap(false) {
		s.closeVideo()
	}
	img := toRGBA(src)
	wantVideo := s.a.whyNoVideo() == ""
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

	// Skip a still screen, but only after it has had time to sharpen.
	now := time.Now()
	if !bytesEqualRGBA(s.prev, img) {
		s.changed = now
		s.prev = copyRGBA(s.prev, img)
	} else if !full && now.Sub(s.changed) > videoSettle {
		return nil, nil
	}

	start := time.Now()
	if _, scale := s.a.res.current(); scale < 100 {
		w := max(img.Rect.Dx()*scale/100&^1, 16)
		h := max(img.Rect.Dy()*scale/100&^1, 16)
		s.scaled = stream.Downscale(s.scaled, img, w, h)
		img = s.scaled
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
			s.a.turnOffVideo(err)
			return s.encode(img, seq, true)
		}
		s.video = v
		full = true
	}

	data, key, err := s.video.Encode(img, full)
	s.a.res.encoded(time.Since(start), time.Now())
	if err != nil {
		log.Printf("H.264 failed, sending JPEG: %v", err)
		s.a.turnOffVideo(err)
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

// whyNoVideo says why the host sends JPEG tiles instead of H.264, or ""
// when it sends video.
func (a *Agent) whyNoVideo() string {
	if a.NewVideo == nil {
		return "H.264 is turned off on the host"
	}
	a.videoMu.Lock()
	why := a.videoErr
	a.videoMu.Unlock()
	if why != "" {
		return why
	}
	if a.VideoStatus != nil {
		if why := a.VideoStatus(); why != "" {
			return why
		}
	}
	if !a.flow.allVideo() {
		return "a viewer's browser cannot decode H.264"
	}
	return ""
}

// turnOffVideo stays on JPEG tiles for the rest of the session.
func (a *Agent) turnOffVideo(err error) {
	a.videoMu.Lock()
	a.videoErr = "H.264 failed on the host: " + err.Error()
	a.videoMu.Unlock()
}

// sendQuality tells viewers which quality is in use and at what scale.
func (a *Agent) sendQuality(ctx context.Context, conn *websocket.Conn) error {
	mode, scale := a.res.current()
	return send(ctx, conn, protocol.Message{Type: protocol.TypeQuality, Text: mode, Scale: scale})
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

package phone

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Options tune the phone's video.
type Options struct {
	MaxSize int // longest side in pixels; 0 keeps the phone's own
	BitRate int // bits per second
	MaxFPS  int
	// Baseline asks for H.264 Constrained Baseline, for decoders
	// (OpenH264) that do not handle every profile phones encode.
	Baseline bool
}

// EventKind says what an Event carries.
type EventKind int

const (
	// EventSize: the video is now Width×Height, at the start and when the
	// phone rotates or unfolds. Pointer positions are measured against it.
	EventSize EventKind = iota
	// EventFrame: one H.264 access unit in Data, with the codec setup
	// that came before it prepended.
	EventFrame
	// EventClipboard: the phone's clipboard changed to Text.
	EventClipboard
	// EventEnded: the session is over, Err says why.
	EventEnded
)

// Event is something the phone sent.
type Event struct {
	Kind          EventKind
	Width, Height int
	Data          []byte
	Key           bool
	Text          string
	Err           error
}

// Session is a running mirror of the phone.
type Session struct {
	Name string // the phone's model name

	video, control net.Conn
	shell          interface{ Kill() error }
	events         chan Event
	end            sync.Once

	mu      sync.Mutex // serializes writes to control
	w, h    int        // current video size, guarded by mu
	clipSeq uint64
}

// Start runs scrcpy's server on the phone and connects to it.
func Start(ctx context.Context, a *ADB, serial string, opt Options) (*Session, error) {
	jar, err := ServerPath(ctx)
	if err != nil {
		return nil, err
	}
	if err := a.Push(ctx, serial, jar, serverRemote); err != nil {
		return nil, err
	}
	scid := rand.Uint32N(0x7fffffff)
	socket := fmt.Sprintf("scrcpy_%08x", scid)
	port, err := a.Forward(ctx, serial, socket)
	if err != nil {
		return nil, err
	}
	defer a.RemoveForward(context.Background(), serial, port)

	args := []string{"-s", serial, "shell", "CLASSPATH=" + serverRemote, "app_process", "/",
		"com.genymobile.scrcpy.Server", ServerVersion,
		fmt.Sprintf("scid=%08x", scid), "log_level=info", "audio=false", "tunnel_forward=true",
		"video_codec=h264", "clipboard_autosync=true"}
	if opt.MaxSize > 0 {
		args = append(args, "max_size="+strconv.Itoa(opt.MaxSize))
	}
	if opt.BitRate > 0 {
		args = append(args, "video_bit_rate="+strconv.Itoa(opt.BitRate))
	}
	if opt.MaxFPS > 0 {
		args = append(args, "max_fps="+strconv.Itoa(opt.MaxFPS))
	}
	if opt.Baseline {
		args = append(args, "video_codec_options=profile=1") // AVCProfileBaseline
	}
	// The server lives as long as this adb shell; its log goes to ours.
	shell := a.command(context.Background(), args...)
	out, err := shell.StdoutPipe()
	if err != nil {
		return nil, err
	}
	shell.Stderr = shell.Stdout
	if err := shell.Start(); err != nil {
		return nil, fmt.Errorf("start the server on the phone: %w", err)
	}
	lines := make(chan string, 16)
	go func() {
		sc := bufio.NewScanner(out)
		for sc.Scan() {
			log.Printf("phone: %s", sc.Text())
			select {
			case lines <- sc.Text():
			default:
			}
		}
		shell.Wait()
		close(lines)
	}()

	s := &Session{shell: shell.Process, events: make(chan Event, 64)}
	fail := func(err error) (*Session, error) {
		s.close()
		// The server says why it stopped, if it did.
		why := ""
		deadline := time.After(500 * time.Millisecond)
	wait:
		for why == "" {
			select {
			case l, ok := <-lines:
				if !ok {
					break wait
				}
				if strings.Contains(l, "ERROR") || strings.Contains(l, "Exception") {
					why = strings.TrimSpace(l)
				}
			case <-deadline:
				break wait
			}
		}
		if why != "" {
			return nil, fmt.Errorf("%w (%s)", err, why)
		}
		return nil, err
	}

	// adb accepts the local connection even before the server listens
	// behind it, so the first byte is what proves the server is there.
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	for i := 0; ; i++ {
		c, err := net.DialTimeout("tcp", addr, 2*time.Second)
		if err == nil {
			c.SetReadDeadline(time.Now().Add(2 * time.Second))
			var b [1]byte
			if _, err = io.ReadFull(c, b[:]); err == nil {
				c.SetReadDeadline(time.Time{})
				s.video = c
				break
			}
			c.Close()
		}
		if i == 100 || ctx.Err() != nil {
			return fail(errors.New("the server on the phone did not start"))
		}
		time.Sleep(100 * time.Millisecond)
	}
	if s.control, err = net.DialTimeout("tcp", addr, 5*time.Second); err != nil {
		return fail(fmt.Errorf("control connection: %w", err))
	}
	if tc, ok := s.control.(*net.TCPConn); ok {
		tc.SetNoDelay(true)
	}

	// Device name, codec, then the first session header with the size.
	s.video.SetReadDeadline(time.Now().Add(10 * time.Second))
	var name [64]byte
	if _, err := io.ReadFull(s.video, name[:]); err != nil {
		return fail(fmt.Errorf("read the phone's name: %w", err))
	}
	s.Name = cString(name[:])
	var codec [4]byte
	if _, err := io.ReadFull(s.video, codec[:]); err != nil {
		return fail(fmt.Errorf("read the video codec: %w", err))
	}
	switch id := binary.BigEndian.Uint32(codec[:]); id {
	case 0x68323634: // "h264"
	case 0, 1:
		return fail(errors.New("the phone could not start screen capture"))
	default:
		return fail(fmt.Errorf("unexpected video codec %08x", id))
	}
	s.video.SetReadDeadline(time.Time{})

	go s.readVideo()
	go s.readDevice()
	return s, nil
}

func cString(b []byte) string {
	for i, c := range b {
		if c == 0 {
			return string(b[:i])
		}
	}
	return string(b)
}

// Events delivers what the phone sends, ending with EventEnded.
func (s *Session) Events() <-chan Event { return s.events }

// Close ends the session; the server on the phone exits with it.
func (s *Session) Close() { s.stop(nil) }

func (s *Session) close() {
	if s.video != nil {
		s.video.Close()
	}
	if s.control != nil {
		s.control.Close()
	}
	if s.shell != nil {
		s.shell.Kill()
	}
}

func (s *Session) stop(err error) {
	s.end.Do(func() {
		s.close()
		if err == nil {
			err = errors.New("closed")
		}
		// Keep the channel open: the readers may still be sending.
		go func() { s.events <- Event{Kind: EventEnded, Err: err} }()
	})
}

func (s *Session) readVideo() {
	r := bufio.NewReaderSize(s.video, 256<<10)
	var config []byte
	var hdr [12]byte
	for {
		if _, err := io.ReadFull(r, hdr[:]); err != nil {
			s.stop(streamErr(err))
			return
		}
		ev, isConfig, err := parseHeader(hdr[:])
		if err != nil {
			s.stop(err)
			return
		}
		if ev.Kind == EventSize {
			s.mu.Lock()
			s.w, s.h = ev.Width, ev.Height
			s.mu.Unlock()
			s.events <- ev
			continue
		}
		data := make([]byte, len(config)+len(ev.Data))
		copy(data, config)
		if _, err := io.ReadFull(r, data[len(config):]); err != nil {
			s.stop(streamErr(err))
			return
		}
		if isConfig {
			// SPS and PPS go in front of the next picture, which decoders
			// take as one access unit.
			config = data
			continue
		}
		config = nil
		ev.Data = data
		s.events <- ev
	}
}

func streamErr(err error) error {
	if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
		return errors.New("the phone ended the session")
	}
	return err
}

// parseHeader reads a 12-byte packet header. For media packets Data is
// allocated to the packet's size, unfilled.
func parseHeader(b []byte) (ev Event, config bool, err error) {
	if b[0]&0x80 != 0 { // session packet
		w, h := binary.BigEndian.Uint32(b[4:]), binary.BigEndian.Uint32(b[8:])
		if w == 0 || h == 0 || w > 1<<15 || h > 1<<15 {
			return ev, false, fmt.Errorf("bad video size %dx%d", w, h)
		}
		return Event{Kind: EventSize, Width: int(w), Height: int(h)}, false, nil
	}
	flags := binary.BigEndian.Uint64(b)
	n := binary.BigEndian.Uint32(b[8:])
	if n == 0 || n > 32<<20 {
		return ev, false, fmt.Errorf("bad packet size %d", n)
	}
	return Event{Kind: EventFrame, Data: make([]byte, n), Key: flags&(1<<61) != 0}, flags&(1<<62) != 0, nil
}

// readDevice reads the phone's messages on the control connection.
func (s *Session) readDevice() {
	r := bufio.NewReader(s.control)
	for {
		ev, err := readDeviceMsg(r)
		if err != nil {
			s.stop(streamErr(err))
			return
		}
		if ev != nil {
			s.events <- *ev
		}
	}
}

// readDeviceMsg reads one message from the phone; only clipboard changes
// give an Event.
func readDeviceMsg(r io.Reader) (*Event, error) {
	var t [1]byte
	if _, err := io.ReadFull(r, t[:]); err != nil {
		return nil, err
	}
	switch t[0] {
	case 0: // clipboard
		var n [4]byte
		if _, err := io.ReadFull(r, n[:]); err != nil {
			return nil, err
		}
		size := binary.BigEndian.Uint32(n[:])
		if size > 1<<18 {
			return nil, fmt.Errorf("clipboard message too large (%d bytes)", size)
		}
		text := make([]byte, size)
		if _, err := io.ReadFull(r, text); err != nil {
			return nil, err
		}
		return &Event{Kind: EventClipboard, Text: string(text)}, nil
	case 1: // clipboard set acknowledged
		var seq [8]byte
		_, err := io.ReadFull(r, seq[:])
		return nil, err
	case 2: // HID output (keyboard LEDs); unused
		var hdr [4]byte
		if _, err := io.ReadFull(r, hdr[:]); err != nil {
			return nil, err
		}
		_, err := io.CopyN(io.Discard, r, int64(binary.BigEndian.Uint16(hdr[2:])))
		return nil, err
	default:
		return nil, fmt.Errorf("unknown message %d from the phone", t[0])
	}
}

// Size is the current video size.
func (s *Session) Size() (int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w, s.h
}

func (s *Session) send(msgs ...[]byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.control.SetWriteDeadline(time.Now().Add(5 * time.Second))
	for _, m := range msgs {
		if _, err := s.control.Write(m); err != nil {
			go s.stop(fmt.Errorf("send to the phone: %w", err))
			return
		}
	}
}

// Touch sends a touch (left mouse button) at nx, ny in 0..1 of the screen.
func (s *Session) Touch(action int, nx, ny float64) {
	w, h := s.Size()
	if w == 0 {
		return
	}
	s.send(touchMsg(action, scale(nx, w), scale(ny, h), w, h))
}

// Scroll scrolls at nx, ny by wheel notches (positive dy scrolls up).
func (s *Session) Scroll(nx, ny, dx, dy float64) {
	w, h := s.Size()
	if w == 0 {
		return
	}
	s.send(scrollMsg(scale(nx, w), scale(ny, h), w, h, dx, dy))
}

func scale(n float64, size int) int { return min(max(int(n*float64(size)), 0), size-1) }

// Key sends an Android key event.
func (s *Session) Key(action, keycode, repeat, meta int) {
	s.send(keycodeMsg(action, keycode, repeat, meta))
}

// Press presses and releases a key.
func (s *Session) Press(keycode int) {
	s.send(keycodeMsg(ActionDown, keycode, 0, 0), keycodeMsg(ActionUp, keycode, 0, 0))
}

// Text types text.
func (s *Session) Text(text string) { s.send(textMsgs(text)...) }

// Back presses BACK, or wakes the screen if it is off.
func (s *Session) Back() { s.send(backMsg(ActionDown), backMsg(ActionUp)) }

// Paste puts text on the phone's clipboard and pastes it.
func (s *Session) Paste(text string) {
	s.mu.Lock()
	s.clipSeq++
	seq := s.clipSeq
	s.mu.Unlock()
	s.send(setClipboardMsg(seq, text, true))
}

// DisplayPower turns the phone's own screen off or on while mirroring
// carries on.
func (s *Session) DisplayPower(on bool) { s.send(displayPowerMsg(on)) }

// Notifications pulls down (or with false, closes) the notification shade.
func (s *Session) Notifications(open bool) {
	if open {
		s.send([]byte{msgExpandNotifications})
	} else {
		s.send([]byte{msgCollapsePanels})
	}
}

// Rotate turns the phone's screen a quarter.
func (s *Session) Rotate() { s.send([]byte{msgRotateDevice}) }

// ResetVideo asks for a fresh key frame, after a frame failed to decode.
func (s *Session) ResetVideo() { s.send([]byte{msgResetVideo}) }

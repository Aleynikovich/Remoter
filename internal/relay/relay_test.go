package relay_test

import (
	"bytes"
	"context"
	"encoding/json"
	"image/jpeg"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/aleynikovich/remoter/internal/capture"
	"github.com/aleynikovich/remoter/internal/host"
	"github.com/aleynikovich/remoter/internal/protocol"
	"github.com/aleynikovich/remoter/internal/relay"
	"github.com/aleynikovich/remoter/internal/stream"
)

type inputRecorder chan protocol.Input

func (r inputRecorder) HandleInput(_ string, in protocol.Input) { r <- in }

type releaseRecorder chan string

func (r releaseRecorder) HandleInput(string, protocol.Input) {}
func (r releaseRecorder) ReleaseAll(viewer string)           { r <- viewer }

// fakeClipboard is an in-memory clipboard.
type fakeClipboard struct {
	mu   sync.Mutex
	seq  uint32
	text string
}

func (c *fakeClipboard) Seq() uint32 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.seq
}

func (c *fakeClipboard) ReadText() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.text, nil
}

func (c *fakeClipboard) WriteText(s string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seq++
	c.text = s
	return nil
}

// startHost runs a relay and a host agent and returns the relay's ws URL and
// the host's session ID.
func startHost(t *testing.T, password string, input host.InputHandler) (string, string) {
	return startHostWith(t, &host.Agent{Password: password, Input: input})
}

func startHostWith(t *testing.T, a *host.Agent) (string, string) {
	t.Helper()
	srv := httptest.NewServer(relay.New().Handler())
	t.Cleanup(srv.Close)
	base := "ws" + strings.TrimPrefix(srv.URL, "http")

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ids := make(chan string, 1)
	a.ServerURL = base + "/ws/host"
	a.Source = capture.TestPattern{Width: 64, Height: 48}
	a.FPS = 30
	a.OnRegistered = func(id string) { ids <- id }
	go a.Run(ctx)
	select {
	case id := <-ids:
		return base, id
	case <-time.After(5 * time.Second):
		t.Fatal("host did not register")
		return "", ""
	}
}

func dialViewer(t *testing.T, base, id, password string) (*websocket.Conn, protocol.Message) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, base+"/ws/viewer", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.CloseNow() })
	c.SetReadLimit(16 << 20)
	req, _ := json.Marshal(protocol.Message{Type: protocol.TypeConnect, ID: id, Password: password})
	if err := c.Write(ctx, websocket.MessageText, req); err != nil {
		t.Fatal(err)
	}
	_, data, err := c.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var m protocol.Message
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	return c, m
}

func TestViewerReceivesFramesAndSendsInput(t *testing.T) {
	inputs := make(inputRecorder, 1)
	base, id := startHost(t, "secret", inputs)
	if len(id) != 9 {
		t.Fatalf("id %q is not 9 digits", id)
	}

	c, m := dialViewer(t, base, id, "secret")
	if m.Type != protocol.TypeConnected {
		t.Fatalf("got %+v, want connected", m)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sendJSON(t, c, protocol.Message{Type: protocol.TypeRefresh})
	u := readUpdate(t, c)
	if !u.Full() || u.Width != 64 || u.Height != 48 {
		t.Fatalf("first update is %dx%d full=%v, want the whole 64x48 screen", u.Width, u.Height, u.Full())
	}
	img, err := jpeg.Decode(bytes.NewReader(u.Rects[0].JPEG))
	if err != nil {
		t.Fatalf("rect is not a JPEG: %v", err)
	}
	if b := img.Bounds(); b.Dx() != u.Rects[0].Dx() || b.Dy() != u.Rects[0].Dy() {
		t.Fatalf("rect %v holds a %v image", u.Rects[0].Rectangle, b)
	}

	msg, _ := json.Marshal(protocol.Message{Type: protocol.TypeInput, Input: &protocol.Input{Kind: "mousedown", X: 0.5, Y: 0.25}})
	if err := c.Write(ctx, websocket.MessageText, msg); err != nil {
		t.Fatal(err)
	}
	select {
	case in := <-inputs:
		if in.Kind != "mousedown" || in.X != 0.5 || in.Y != 0.25 {
			t.Fatalf("host got %+v", in)
		}
	case <-ctx.Done():
		t.Fatal("host did not receive input")
	}
}

func sendJSON(t *testing.T, c *websocket.Conn, m protocol.Message) {
	t.Helper()
	data, _ := json.Marshal(m)
	if err := c.Write(context.Background(), websocket.MessageText, data); err != nil {
		t.Fatal(err)
	}
}

func readUpdate(t *testing.T, c *websocket.Conn) *stream.Update {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	typ, data, err := c.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if typ != websocket.MessageBinary {
		t.Fatalf("got message type %v, want binary update", typ)
	}
	u, err := stream.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestHostWaitsForAcks(t *testing.T) {
	base, id := startHost(t, "secret", nil)
	c, _ := dialViewer(t, base, id, "secret")
	sendJSON(t, c, protocol.Message{Type: protocol.TypeRefresh})

	for range 3 {
		readUpdate(t, c)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	_, _, err := c.Read(ctx)
	cancel()
	if err == nil {
		t.Fatal("host sent a 4th update without an ack")
	}
	// The timed-out read closed the connection, so reconnect to check
	// that acks let updates through again.
	c, _ = dialViewer(t, base, id, "secret")
	sendJSON(t, c, protocol.Message{Type: protocol.TypeRefresh})
	for range 10 {
		u := readUpdate(t, c)
		sendJSON(t, c, protocol.Message{Type: protocol.TypeAck, Seq: u.Seq})
	}
}

func TestClipboardIsSharedBothWays(t *testing.T) {
	clip := &fakeClipboard{}
	base, id := startHostWith(t, &host.Agent{Password: "secret", Clipboard: clip})
	c, _ := dialViewer(t, base, id, "secret")
	sendJSON(t, c, protocol.Message{Type: protocol.TypeRefresh})

	sendJSON(t, c, protocol.Message{Type: protocol.TypeClipboard, Text: "from viewer"})
	deadline := time.Now().Add(5 * time.Second)
	for {
		if text, _ := clip.ReadText(); text == "from viewer" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("host clipboard was not set")
		}
		time.Sleep(10 * time.Millisecond)
	}

	clip.WriteText("from host")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		typ, data, err := c.Read(ctx)
		if err != nil {
			t.Fatal("viewer did not get the host clipboard:", err)
		}
		if typ != websocket.MessageText {
			continue // screen update
		}
		var m protocol.Message
		json.Unmarshal(data, &m)
		if m.Type != protocol.TypeClipboard {
			continue
		}
		if m.Text != "from host" {
			t.Fatalf("viewer got clipboard %q, want the host's (and no echo of its own)", m.Text)
		}
		return
	}
}

type fakeMonitors struct {
	mu      sync.Mutex
	current int
}

func (m *fakeMonitors) Count() int { return 2 }

func (m *fakeMonitors) Current() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.current
}

func (m *fakeMonitors) Select(d int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.current = d
	return nil
}

// readText returns the next control message of the given type, skipping
// screen updates and other messages.
func readText(t *testing.T, c *websocket.Conn, typ string) protocol.Message {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		mt, data, err := c.Read(ctx)
		if err != nil {
			t.Fatalf("no %s message: %v", typ, err)
		}
		var m protocol.Message
		if mt == websocket.MessageText && json.Unmarshal(data, &m) == nil && m.Type == typ {
			return m
		}
	}
}

func TestCursorAndDisplaySwitching(t *testing.T) {
	mons := &fakeMonitors{}
	base, id := startHostWith(t, &host.Agent{
		Password: "secret",
		Cursor:   func() string { return "text" },
		Monitors: mons,
	})
	c, _ := dialViewer(t, base, id, "secret")
	sendJSON(t, c, protocol.Message{Type: protocol.TypeRefresh})

	// The display list (sent on join) and the cursor (sent with the first
	// update) can arrive in either order.
	var displays, cursor *protocol.Message
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for displays == nil || cursor == nil {
		mt, data, err := c.Read(ctx)
		if err != nil {
			t.Fatalf("displays %v, cursor %v: %v", displays, cursor, err)
		}
		var m protocol.Message
		if mt != websocket.MessageText || json.Unmarshal(data, &m) != nil {
			continue
		}
		switch m.Type {
		case protocol.TypeDisplays:
			displays = &m
		case protocol.TypeCursor:
			cursor = &m
		}
	}
	if displays.Count != 2 || displays.Display != 0 {
		t.Fatalf("got %+v, want 2 displays showing the first", displays)
	}
	if cursor.Cursor != "text" {
		t.Fatalf("got cursor %q, want text", cursor.Cursor)
	}

	sendJSON(t, c, protocol.Message{Type: protocol.TypeSelectDisplay, Display: 1})
	if m := readText(t, c, protocol.TypeDisplays); m.Display != 1 || mons.Current() != 1 {
		t.Fatalf("got %+v, want the second display selected", m)
	}
}

func TestWrongPasswordIsRejected(t *testing.T) {
	base, id := startHost(t, "secret", nil)
	_, m := dialViewer(t, base, id, "nope")
	if m.Type != protocol.TypeError || m.Error != "wrong password" {
		t.Fatalf("got %+v, want wrong password error", m)
	}
}

func TestEmptyIDConnectsToOnlyHost(t *testing.T) {
	base, _ := startHost(t, "root", nil)
	if _, m := dialViewer(t, base, "", "root"); m.Type != protocol.TypeConnected {
		t.Fatalf("got %+v, want connected", m)
	}
}

func TestUnknownID(t *testing.T) {
	base, _ := startHost(t, "secret", nil)
	_, m := dialViewer(t, base, "000000000", "secret")
	if m.Type != protocol.TypeError {
		t.Fatalf("got %+v, want error", m)
	}
}

func TestLockoutAfterRepeatedFailures(t *testing.T) {
	base, id := startHost(t, "secret", nil)
	for range 5 {
		dialViewer(t, base, id, "nope")
	}
	_, m := dialViewer(t, base, id, "secret")
	if m.Type != protocol.TypeError || !strings.Contains(m.Error, "too many") {
		t.Fatalf("got %+v, want lockout", m)
	}
}

func TestHeldInputReleasedWhenViewerLeaves(t *testing.T) {
	released := make(releaseRecorder, 1)
	base, id := startHost(t, "secret", released)
	c, m := dialViewer(t, base, id, "secret")
	if m.Type != protocol.TypeConnected {
		t.Fatalf("got %+v, want connected", m)
	}
	c.Close(websocket.StatusNormalClosure, "")
	select {
	case <-released:
	case <-time.After(5 * time.Second):
		t.Fatal("host did not release input after the viewer left")
	}
}

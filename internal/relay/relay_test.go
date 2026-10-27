package relay_test

import (
	"bytes"
	"context"
	"encoding/json"
	"image/jpeg"
	"net/http/httptest"
	"strings"
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

// startHost runs a relay and a host agent and returns the relay's ws URL and
// the host's session ID.
func startHost(t *testing.T, password string, input host.InputHandler) (string, string) {
	t.Helper()
	srv := httptest.NewServer(relay.New().Handler())
	t.Cleanup(srv.Close)
	base := "ws" + strings.TrimPrefix(srv.URL, "http")

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ids := make(chan string, 1)
	a := &host.Agent{
		ServerURL:    base + "/ws/host",
		Password:     password,
		Source:       capture.TestPattern{Width: 64, Height: 48},
		Input:        input,
		FPS:          30,
		OnRegistered: func(id string) { ids <- id },
	}
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

func TestWrongPasswordIsRejected(t *testing.T) {
	base, id := startHost(t, "secret", nil)
	_, m := dialViewer(t, base, id, "nope")
	if m.Type != protocol.TypeError || m.Error != "wrong password" {
		t.Fatalf("got %+v, want wrong password error", m)
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

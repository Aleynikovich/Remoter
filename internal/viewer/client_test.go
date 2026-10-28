package viewer_test

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aleynikovich/remoter/internal/capture"
	"github.com/aleynikovich/remoter/internal/host"
	"github.com/aleynikovich/remoter/internal/protocol"
	"github.com/aleynikovich/remoter/internal/relay"
	"github.com/aleynikovich/remoter/internal/stream"
	"github.com/aleynikovich/remoter/internal/viewer"
)

type inputRecorder chan protocol.Input

func (r inputRecorder) HandleInput(_ string, in protocol.Input) { r <- in }

// startHost runs a relay with one test-pattern host and returns the relay's
// address as a person would type it.
func startHost(t *testing.T, input host.InputHandler) string {
	t.Helper()
	srv := httptest.NewServer(relay.New().Handler())
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	registered := make(chan string, 1)
	a := &host.Agent{
		ServerURL:    "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws/host",
		Password:     "root",
		Input:        input,
		Source:       capture.TestPattern{Width: 64, Height: 48},
		FPS:          30,
		OnRegistered: func(id string) { registered <- id },
	}
	go a.Run(ctx)
	select {
	case <-registered:
	case <-time.After(5 * time.Second):
		t.Fatal("host did not register")
	}
	return strings.TrimPrefix(srv.URL, "http://")
}

func next(t *testing.T, c *viewer.Client, kind viewer.EventKind) viewer.Event {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case ev := <-c.Events():
			if ev.Kind == kind {
				return ev
			}
			if ev.Kind == viewer.EventEnded {
				t.Fatalf("session ended: %s", ev.Text)
			}
		case <-timeout:
			t.Fatalf("no event of kind %d", kind)
		}
	}
}

func TestClientSession(t *testing.T) {
	inputs := make(inputRecorder, 16)
	c, err := viewer.NewClient(startHost(t, inputs), "", "root")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)

	next(t, c, viewer.EventConnected)
	c.Refresh(false)
	frame := next(t, c, viewer.EventFrame)
	u, err := stream.Parse(frame.Data)
	if err != nil || u.Width != 64 || u.Height != 48 {
		t.Fatalf("first frame: %v, %+v", err, u)
	}
	c.Ack(u.Seq)

	c.Input(protocol.Input{Kind: "keydown", Code: "KeyA"})
	select {
	case in := <-inputs:
		if in.Kind != "keydown" || in.Code != "KeyA" {
			t.Errorf("host got %+v", in)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("input did not reach the host")
	}

	cancel()
	if ev := next(t, c, viewer.EventEnded); ev.Text != "Disconnected." {
		t.Errorf("ended with %q", ev.Text)
	}
}

func TestClientWrongPassword(t *testing.T) {
	c, err := viewer.NewClient(startHost(t, nil), "", "wrong")
	if err != nil {
		t.Fatal(err)
	}
	go c.Run(context.Background())
	select {
	case ev := <-c.Events():
		if ev.Kind != viewer.EventEnded || !strings.Contains(strings.ToLower(ev.Text), "password") {
			t.Errorf("got %+v, want the session to end over the password", ev)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("wrong password was not reported")
	}
}

func TestHTTPBase(t *testing.T) {
	for in, want := range map[string]string{
		"ws://100.102.208.35:8080/ws/viewer": "http://100.102.208.35:8080",
		"wss://relay.example/ws/viewer":      "https://relay.example",
	} {
		if got := viewer.HTTPBase(in); got != want {
			t.Errorf("HTTPBase(%q) = %q, want %q", in, got, want)
		}
	}
}

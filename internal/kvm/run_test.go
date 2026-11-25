package kvm

import (
	"context"
	"image"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	screencap "github.com/aleynikovich/remoter/internal/capture"
	"github.com/aleynikovich/remoter/internal/host"
	"github.com/aleynikovich/remoter/internal/protocol"
	"github.com/aleynikovich/remoter/internal/relay"
)

// fakeCapture stands in for the Windows hooks.
type fakeCapture struct {
	mu    sync.Mutex
	m     *Machine
	warps []image.Point
	ready chan struct{}
}

func (f *fakeCapture) Warp(p image.Point) {
	f.mu.Lock()
	f.warps = append(f.warps, p)
	f.mu.Unlock()
}
func (f *fakeCapture) Screens() []image.Rectangle {
	return []image.Rectangle{image.Rect(0, 0, 1920, 1080)}
}
func (f *fakeCapture) OnUserDesktop() bool { return true }
func (f *fakeCapture) Run(ctx context.Context, m *Machine) error {
	f.mu.Lock()
	f.m = m
	f.mu.Unlock()
	close(f.ready)
	<-ctx.Done()
	return nil
}

type hostKVM struct {
	inputs   chan protocol.Input
	released chan string
}

func (k *hostKVM) HandleInput(_ string, in protocol.Input) { k.inputs <- in }
func (k *hostKVM) ReleaseAll(v string)                     { k.released <- v }
func (k *hostKVM) Screens() []image.Rectangle {
	return []image.Rectangle{image.Rect(0, 0, 2560, 1440)}
}

func TestRunDrivesAHost(t *testing.T) {
	srv := httptest.NewServer(relay.New().Handler())
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	k := &hostKVM{inputs: make(chan protocol.Input, 64), released: make(chan string, 8)}
	a := &host.Agent{
		ServerURL: "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws/host",
		Password:  "pw", Input: host.LogInput{}, KVM: k,
		Source: screencap.TestPattern{Width: 64, Height: 48},
	}
	registered := make(chan string, 1)
	a.OnRegistered = func(id string) { registered <- id }
	go a.Run(ctx)
	<-registered

	fc := &fakeCapture{ready: make(chan struct{})}
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, Options{Addr: strings.TrimPrefix(srv.URL, "http://"), Password: "pw", Config: Config{Screen: -1}}, fc)
	}()
	<-fc.ready
	// Wait for the host's screens, then push off the bottom.
	deadline := time.Now().Add(10 * time.Second)
	fc.mu.Lock()
	m := fc.m
	fc.mu.Unlock()
	for !m.Remote() {
		if time.Now().After(deadline) {
			t.Fatal("never crossed: no screens from the host?")
		}
		m.Mouse(MouseEvent{Kind: MouseMove, Pt: image.Pt(960, 1079)})
		time.Sleep(20 * time.Millisecond)
	}
	m.Key(KeyEvent{Down: true, Scan: 0x1E, VK: 'A'})
	want := []string{"mousemove", "keydown"}
	for len(want) > 0 {
		select {
		case in := <-k.inputs:
			if in.Kind == want[0] {
				want = want[1:]
			}
		case <-ctx.Done():
			t.Fatalf("host never got %v", want)
		}
	}
	m.Return("test")
	select {
	case <-k.released:
	case <-ctx.Done():
		t.Fatal("host did not let go of the held key")
	}
	cancel()
	<-done
}

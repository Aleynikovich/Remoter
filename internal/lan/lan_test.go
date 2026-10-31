package lan

import (
	"context"
	"net/netip"
	"testing"
	"time"
)

func TestLocal(t *testing.T) {
	for ip, want := range map[string]bool{
		"192.168.1.5": true, "10.0.0.2": true, "172.20.1.1": true, "127.0.0.1": true,
		"169.254.3.4": true, "100.102.208.35": false, "8.8.8.8": false, "::ffff:192.168.1.5": true,
	} {
		if got := Local(netip.MustParseAddr(ip)); got != want {
			t.Errorf("Local(%s) = %v, want %v", ip, got, want)
		}
	}
}

func TestDiscoverFindsServer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errc := make(chan error, 1)
	go func() {
		errc <- Serve(ctx, Port, func() Answer {
			return Answer{Name: "desk", OS: "windows", Port: 8080, Remoter: "dev-1", Tailnet: "100.1.2.3"}
		})
	}()
	time.Sleep(100 * time.Millisecond)
	select {
	case err := <-errc:
		t.Skipf("cannot listen on UDP %d here: %v", Port, err)
	default:
	}
	// The test machine may have no private network to broadcast on.
	extraTargets = []netip.Addr{netip.MustParseAddr("127.0.0.1")}
	defer func() { extraTargets = nil }()
	answers, err := Discover(ctx, 600*time.Millisecond)
	if err != nil {
		t.Skipf("no broadcast here: %v", err)
	}
	if len(answers) != 1 {
		t.Fatalf("got %d answers, want 1: %+v", len(answers), answers)
	}
	a := answers[0]
	if a.Name != "desk" || a.Remoter != "dev-1" || a.Tailnet != "100.1.2.3" || a.IP == "" {
		t.Errorf("answer = %+v", a)
	}
	if !Local(netip.MustParseAddr(a.IP)) {
		t.Errorf("answer from %s, want a local address", a.IP)
	}
}

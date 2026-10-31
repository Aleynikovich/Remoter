package host

import (
	"testing"
	"time"
)

func TestSlowViewerDoesNotFreezeOthers(t *testing.T) {
	f := flow{viewers: make(map[string]*viewerState), wake: make(chan struct{}, 1)}
	f.join("fast", true)
	f.join("slow", true)
	for seq := uint32(1); seq <= window; seq++ {
		if !f.ready() {
			t.Fatalf("not ready before update %d", seq)
		}
		f.sent(seq, 1000)
		f.ack("fast", seq)
	}
	if f.ready() {
		t.Fatal("should wait a moment for the slow viewer")
	}
	f.viewers["slow"].full = time.Now().Add(-lagAfter)
	if !f.ready() {
		t.Fatal("still waiting for the slow viewer after lagAfter")
	}
	f.ack("slow", window)
	f.viewers["fast"].acked = 0
	f.viewers["slow"].acked = 0
	f.viewers["fast"].full, f.viewers["slow"].full = time.Now().Add(-time.Minute), time.Now().Add(-time.Minute)
	if f.ready() {
		t.Fatal("ready with no viewer having room")
	}
}

func TestLaggingViewerStaysIgnoredUntilCaughtUp(t *testing.T) {
	f := flow{viewers: make(map[string]*viewerState), wake: make(chan struct{}, 1)}
	f.join("fast", true)
	f.join("phone", true)
	seq := uint32(0)
	send := func() {
		seq++
		f.sent(seq, 1000)
		f.ack("fast", seq)
	}
	for range window {
		send()
	}
	f.ready()
	f.viewers["phone"].full = time.Now().Add(-lagAfter)
	if !f.ready() || !f.viewers["phone"].lagging {
		t.Fatal("phone should be marked lagging")
	}
	// The phone acks now and then, which briefly gives it room; the host
	// must not start waiting for it again.
	for range 20 {
		send()
		f.ack("phone", f.viewers["phone"].acked+1)
		if !f.ready() {
			t.Fatalf("waiting for the lagging phone at seq %d", seq)
		}
	}
	f.ack("phone", seq)
	f.viewers["phone"].laggingAt = time.Now().Add(-lagPeriod)
	f.ready()
	if f.viewers["phone"].lagging {
		t.Fatal("phone caught up but is still marked lagging")
	}
}

// A viewer that acks in bursts (here every 250 ms) used to hold the host to
// three frames per burst.
func TestBurstyAckerIsNotWaitedFor(t *testing.T) {
	f := flow{viewers: make(map[string]*viewerState), wake: make(chan struct{}, 1)}
	f.join("fast", true)
	f.join("bursty", true)
	start := time.Now()
	now := start
	seq := uint32(0)
	for now.Sub(start) < 3*time.Second {
		if f.ready() {
			seq++
			f.sent(seq, 1000)
			f.ack("fast", seq)
		}
		time.Sleep(5 * time.Millisecond)
		now = time.Now()
		if int(now.Sub(start)/(250*time.Millisecond)) > int(now.Add(-5*time.Millisecond).Sub(start)/(250*time.Millisecond)) {
			f.ack("bursty", seq)
		}
	}
	if fps := float64(seq) / now.Sub(start).Seconds(); fps < 100 {
		t.Fatalf("%.0f updates a second; the bursty viewer is still setting the pace", fps)
	}
}

// On a phone network a round trip can take a second. While the link has
// bandwidth to spare, the host must not send only a window per round trip.
func TestLongLinkKeepsTheFrameRate(t *testing.T) {
	f := flow{viewers: make(map[string]*viewerState), wake: make(chan struct{}, 1)}
	f.join("far", true)
	const rtt = 500 * time.Millisecond
	start := time.Now()
	seq := uint32(0)
	type pending struct {
		seq uint32
		due time.Time
	}
	var acks []pending
	for time.Since(start) < 3*time.Second {
		now := time.Now()
		for len(acks) > 0 && !acks[0].due.After(now) {
			f.ack("far", acks[0].seq)
			acks = acks[1:]
		}
		if f.ready() {
			seq++
			f.sent(seq, 2000) // 30 fps of these is 0.5 Mbit/s
			acks = append(acks, pending{seq, now.Add(rtt)})
		}
		time.Sleep(33 * time.Millisecond)
	}
	// After the first round trips the window opens up to the full 30 fps.
	t.Logf("%.0f updates a second", float64(seq)/3)
	if fps := float64(seq) / 3; fps < 20 {
		t.Fatalf("%.0f updates a second over a %s link", fps, rtt)
	}
}

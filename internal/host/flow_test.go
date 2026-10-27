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
		f.sent(seq)
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

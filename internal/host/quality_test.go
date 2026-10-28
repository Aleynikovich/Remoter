package host

import (
	"testing"
	"time"
)

// encodeAt feeds q frames whose encoding time grows with the pixel count,
// as a real encoder's does: full is the time at 100%.
func encodeAt(q *quality, full time.Duration, start time.Time, n int) time.Time {
	now := start
	for range n {
		_, s := q.current()
		q.encoded(full*time.Duration(s*s)/10000, now)
		now = now.Add(frameBudget)
	}
	return now
}

func TestAutoQualityFindsTheScaleThatKeepsUp(t *testing.T) {
	q := newQuality()
	now := encodeAt(q, 25*time.Millisecond, time.Now(), 600)
	_, s := q.current()
	// 25 ms at 100% fits the 16.7 ms budget at about 80%.
	if s < 70 || s > 80 {
		t.Fatalf("slow encoding: scale %d, want 70-80", s)
	}
	// A heavier scene shrinks it further, never below minScale.
	now = encodeAt(q, 200*time.Millisecond, now, 600)
	if _, s := q.current(); s != minScale {
		t.Fatalf("very slow encoding: scale %d, want %d", s, minScale)
	}
	// And a light one lets it grow back to full size.
	encodeAt(q, 5*time.Millisecond, now, 1200)
	if _, s := q.current(); s != 100 {
		t.Fatalf("fast encoding: scale %d, want 100", s)
	}
}

func TestAutoQualityHoldsSteady(t *testing.T) {
	q := newQuality()
	now := encodeAt(q, 25*time.Millisecond, time.Now(), 600)
	_, settled := q.current()
	changes := 0
	for range 20 {
		now = encodeAt(q, 25*time.Millisecond, now, 60)
		if _, s := q.current(); s != settled {
			changes++
			settled = s
		}
	}
	if changes > 0 {
		t.Fatalf("scale changed %d times under a steady load", changes)
	}
}

func TestFixedQuality(t *testing.T) {
	q := newQuality()
	if !q.set("smooth") || q.set("ultra") || q.set("5") {
		t.Fatal("set accepted the wrong modes")
	}
	q.encoded(time.Second, time.Now())
	if m, s := q.current(); m != "smooth" || s != 50 {
		t.Fatalf("fixed quality changed to %s %d", m, s)
	}
	if !q.set("85") {
		t.Fatal("set refused a percentage")
	}
	if _, s := q.current(); s != 85 {
		t.Fatalf("scale %d, want 85", s)
	}
}

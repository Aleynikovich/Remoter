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

func TestAutoQualityShrinksOnASlowLink(t *testing.T) {
	q := newQuality()
	now := time.Now()
	tick := func(waitShare float64, seconds int) {
		for range seconds * 10 {
			q.waited(time.Duration(waitShare*100)*time.Millisecond, 100*time.Millisecond, now)
			q.encoded(5*time.Millisecond, now) // a fast encoder
			now = now.Add(100 * time.Millisecond)
		}
	}
	tick(0.9, 10)
	_, slow := q.current()
	if slow >= 80 {
		t.Fatalf("scale %d after 10 s of waiting on the link", slow)
	}
	tick(0, 60)
	if _, s := q.current(); s != 100 {
		t.Fatalf("scale %d once the link kept up, want 100", s)
	}
}

func TestVideoSize(t *testing.T) {
	for _, c := range []struct{ w, h, scale, ww, wh int }{
		{3200, 1800, 100, 3200, 1800},
		{3200, 1800, 75, 2400, 1350},
		{5120, 1800, 100, 4096, 1440},
		{5120, 1800, 50, 2560, 900},
		{3200, 3600, 100, 2896, 3258},
	} {
		w, h := videoSize(c.w, c.h, c.scale)
		if w != c.ww || h != c.wh {
			t.Errorf("videoSize(%d, %d, %d) = %dx%d, want %dx%d", c.w, c.h, c.scale, w, h, c.ww, c.wh)
		}
		if w > maxVideoSide || h > maxVideoSide || w*h > maxVideoArea {
			t.Errorf("videoSize(%d, %d, %d) = %dx%d, over the limits", c.w, c.h, c.scale, w, h)
		}
	}
}

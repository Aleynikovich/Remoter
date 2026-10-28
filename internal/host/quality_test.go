package host

import (
	"testing"
	"time"
)

func TestAutoQualityStepsDownAndUp(t *testing.T) {
	q := newQuality()
	interval := 33 * time.Millisecond
	now := time.Now()
	for i := range 200 { // 40 ms per frame for a while
		q.encoded(40*time.Millisecond, interval, now.Add(time.Duration(i)*interval))
	}
	if _, s := q.current(); s != 50 {
		t.Fatalf("slow encoding: scale %d, want 50", s)
	}
	now = now.Add(time.Minute)
	for i := range 700 { // then 5 ms per frame
		q.encoded(5*time.Millisecond, interval, now.Add(time.Duration(i)*interval))
	}
	if _, s := q.current(); s != 100 {
		t.Fatalf("fast encoding: scale %d, want 100", s)
	}
	if !q.set("smooth") || q.set("ultra") {
		t.Fatal("set accepted the wrong modes")
	}
	q.encoded(time.Millisecond, interval, now.Add(time.Hour))
	if m, s := q.current(); m != "smooth" || s != 50 {
		t.Fatalf("fixed quality changed to %s %d", m, s)
	}
}

package host

import (
	"sync"
	"time"
)

// Quality settings a viewer can pick. Each one is the share of the full
// resolution that video is encoded at; auto moves between them.
var qualityScales = map[string]int{"sharp": 100, "balanced": 75, "smooth": 50}

var autoSteps = []int{100, 75, 50}

const (
	// Auto steps down when encoding a frame takes more than this share of
	// the frame interval, and back up when it takes less than upShare.
	downShare = 0.9
	upShare   = 0.4
	downAfter = 2 * time.Second
	upAfter   = 8 * time.Second
)

// quality picks the resolution video is encoded at.
type quality struct {
	mu      sync.Mutex
	mode    string // "auto" or a key of qualityScales
	scale   int    // percent of full resolution
	avg     float64
	slow    time.Time // since when encoding has been too slow, or zero
	fast    time.Time // since when it has had plenty of room, or zero
	changed chan struct{}
}

func newQuality() *quality {
	return &quality{mode: "auto", scale: 100, changed: make(chan struct{}, 1)}
}

func (q *quality) notify() {
	select {
	case q.changed <- struct{}{}:
	default:
	}
}

// set switches to mode and reports whether it is a known one.
func (q *quality) set(mode string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	s, fixed := qualityScales[mode]
	if !fixed && mode != "auto" {
		return false
	}
	q.mode = mode
	if fixed {
		q.scale = s
	}
	q.slow, q.fast = time.Time{}, time.Time{}
	q.notify()
	return true
}

func (q *quality) current() (mode string, scale int) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.mode, q.scale
}

// encoded records how long a video frame took to encode at the current
// scale; in auto mode it moves the scale when that stays too slow or fast.
func (q *quality) encoded(took, interval time.Duration, now time.Time) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.avg = 0.8*q.avg + 0.2*float64(took)
	if q.mode != "auto" {
		return
	}
	step := -1
	for i, s := range autoSteps {
		if s == q.scale {
			step = i
		}
	}
	switch share := q.avg / float64(interval); {
	case share > downShare && step < len(autoSteps)-1:
		q.fast = time.Time{}
		if q.slow.IsZero() {
			q.slow = now
		} else if now.Sub(q.slow) >= downAfter {
			q.scale, q.slow = autoSteps[step+1], time.Time{}
			q.avg *= 0.6 // fewer pixels; don't step again on old numbers
			q.notify()
		}
	case share < upShare && step > 0:
		q.slow = time.Time{}
		if q.fast.IsZero() {
			q.fast = now
		} else if now.Sub(q.fast) >= upAfter {
			q.scale, q.fast = autoSteps[step-1], time.Time{}
			q.avg *= 1.7
			q.notify()
		}
	default:
		q.slow, q.fast = time.Time{}, time.Time{}
	}
}

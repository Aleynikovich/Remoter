package host

import (
	"math"
	"strconv"
	"sync"
	"time"
)

// Quality settings a viewer can pick. Each one is the share of the full
// resolution that video is encoded at; a viewer may also send a percentage
// such as "85". Auto picks the scale itself.
var qualityScales = map[string]int{"sharp": 100, "balanced": 75, "smooth": 50}

const (
	// Auto aims to encode at autoFPS: it shrinks the picture while
	// encoding a frame takes more than downShare of a frame's time, and
	// grows it again while there is room left at upShare.
	autoFPS   = 60
	downShare = 0.95
	upShare   = 0.7
	downAfter = time.Second
	upAfter   = 3 * time.Second
	// The scale moves in steps of scaleStep percent, never below
	// minScale, and grows at most upStep at a time so it does not
	// overshoot and fall back.
	scaleStep = 5
	minScale  = 40
	upStep    = 10
	// Auto also shrinks the picture, by netStep every netDownAfter, while
	// the stream spends more than netDownShare of its time waiting for
	// viewers (a slow link), and lets it grow back once that stays under
	// netUpShare for netUpAfter.
	netStep      = 10
	netDownShare = 0.5
	netUpShare   = 0.2
	netDownAfter = 2 * time.Second
	netUpAfter   = 5 * time.Second
)

// frameBudget is the time a frame may take to encode at autoFPS.
const frameBudget = time.Second / autoFPS

// quality picks the resolution video is encoded at.
type quality struct {
	mu      sync.Mutex
	mode    string // "auto", a key of qualityScales or a percentage
	scale   int    // percent of full resolution
	avg     float64
	slow    time.Time // since when encoding has been too slow, or zero
	fast    time.Time // since when it has had room to grow, or zero
	changed chan struct{}

	netCap          int // the most the link allows, in percent
	netWait, netAll time.Duration
	netFrom         time.Time // start of the current second's tally
	netSlow         time.Time // since when the link has held the stream back
	netFast         time.Time // since when it has not
}

func newQuality() *quality {
	return &quality{mode: "auto", scale: 100, netCap: 100, changed: make(chan struct{}, 1)}
}

func (q *quality) notify() {
	select {
	case q.changed <- struct{}{}:
	default:
	}
}

// set switches to mode and reports whether it is a known one.
func (q *quality) set(mode string) bool {
	s, fixed := qualityScales[mode]
	if n, err := strconv.Atoi(mode); err == nil && n >= 10 && n <= 100 {
		s, fixed = n, true
	}
	if !fixed && mode != "auto" {
		return false
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.mode = mode
	q.netCap = 100
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

// fit returns the scale at which encoding would take share of the frame
// budget, given that it took avg at scale. Encoding time grows with the
// number of pixels, the square of the scale.
func fit(scale int, avg float64, share float64) int {
	s := float64(scale) * math.Sqrt(share*float64(frameBudget)/max(avg, 1))
	return min(100, max(minScale, int(s)/scaleStep*scaleStep))
}

// encoded records how long a video frame took to encode at the current
// scale; in auto mode it moves the scale when that stays too slow or fast.
func (q *quality) encoded(took time.Duration, now time.Time) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.avg == 0 {
		q.avg = float64(took)
	}
	q.avg = 0.8*q.avg + 0.2*float64(took)
	if q.mode != "auto" {
		return
	}
	move := func(to int) {
		q.avg *= float64(to*to) / float64(q.scale*q.scale)
		q.scale = to
		q.slow, q.fast = time.Time{}, time.Time{}
		q.notify()
	}
	if q.scale > q.netCap {
		move(q.netCap)
		return
	}
	if down := fit(q.scale, q.avg, downShare); down < q.scale {
		q.fast = time.Time{}
		if q.slow.IsZero() {
			q.slow = now
		} else if now.Sub(q.slow) >= downAfter {
			move(down)
		}
		return
	}
	q.slow = time.Time{}
	if up := min(fit(q.scale, q.avg, upShare), q.netCap); up > q.scale {
		if q.fast.IsZero() {
			q.fast = now
		} else if now.Sub(q.fast) >= upAfter {
			move(min(up, q.scale+upStep))
		}
		return
	}
	q.fast = time.Time{}
}

// waited records that the stream waited wait for viewers out of the total
// time since its last capture. Every second it checks whether the link is
// what holds the stream back, and moves netCap.
func (q *quality) waited(wait, total time.Duration, now time.Time) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.netFrom.IsZero() {
		q.netFrom = now
	}
	q.netWait += wait
	q.netAll += total
	if now.Sub(q.netFrom) < time.Second || q.netAll == 0 {
		return
	}
	share := float64(q.netWait) / float64(q.netAll)
	q.netWait, q.netAll, q.netFrom = 0, 0, now
	if q.mode != "auto" {
		return
	}
	switch {
	case share > netDownShare:
		q.netFast = time.Time{}
		if q.netSlow.IsZero() {
			q.netSlow = now
		} else if now.Sub(q.netSlow) >= netDownAfter && q.netCap > minScale {
			q.netCap = max(minScale, min(q.netCap, q.scale)-netStep)
			q.netSlow = now
		}
	case share < netUpShare:
		q.netSlow = time.Time{}
		if q.netFast.IsZero() {
			q.netFast = now
		} else if now.Sub(q.netFast) >= netUpAfter && q.netCap < 100 {
			q.netCap = min(100, q.netCap+netStep)
			q.netFast = now
		}
	default:
		q.netSlow, q.netFast = time.Time{}, time.Time{}
	}
}

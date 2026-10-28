package capture

import (
	"image"
	"image/draw"
	"slices"
	"time"
)

// All is the display index that shows every monitor at once, laid out as
// they are on the desktop.
const All = -1

const (
	// allWait is how long a capture of every monitor waits for one of
	// them to change before returning the same picture again.
	allWait = 50 * time.Millisecond
	// allRecheck is how often the monitor layout is looked up again.
	allRecheck = time.Second
)

// Union is the smallest rectangle that holds every display.
func Union(displays []image.Rectangle) image.Rectangle {
	var u image.Rectangle
	for _, d := range displays {
		u = u.Union(d)
	}
	return u
}

// waitCapturer is a native source that can wait a given time for the
// screen to change, and say whether it did.
type waitCapturer interface {
	CaptureWait(wait time.Duration) (img image.Image, fresh bool, err error)
}

// allScreens draws every display into one picture. The displays are
// captured one after another on the calling thread (on Windows that
// thread may be kept on the sign-in desktop), and only one of them is
// waited on at a time: the one that changed last, as it is the most
// likely to change again.
type allScreens struct {
	displays []image.Rectangle // as Displays returned them
	screens  []*Screen
	union    image.Rectangle
	checked  time.Time

	gens []uint64       // how many new pictures each display has had
	imgs [2]*image.RGBA // returned in turn, see Source
	drew [2][]uint64    // the generation of each display in each image
	turn int
	last int // the display that changed last
}

func newAllScreens(displays []image.Rectangle) *allScreens {
	a := &allScreens{displays: displays, union: Union(displays), checked: time.Now()}
	for i := range displays {
		a.screens = append(a.screens, NewScreen(i))
	}
	a.gens = make([]uint64, len(displays))
	r := image.Rect(0, 0, a.union.Dx(), a.union.Dy())
	for t := range a.imgs {
		a.imgs[t] = image.NewRGBA(r)
		// Gaps between monitors of different sizes stay black.
		draw.Draw(a.imgs[t], r, image.Black, image.Point{}, draw.Src)
		a.drew[t] = make([]uint64, len(displays))
	}
	return a
}

// stale reports whether the monitor layout changed since a was made.
func (a *allScreens) stale() bool {
	if time.Since(a.checked) < allRecheck {
		return false
	}
	a.checked = time.Now()
	return !slices.Equal(a.displays, Displays())
}

func (a *allScreens) Capture() (image.Image, error) {
	pics := make([]image.Image, len(a.screens))
	var firstErr error
	got := 0
	// Wait on the display that changed last, then take the others as
	// they are.
	order := []int{a.last}
	for i := range a.screens {
		if i != a.last {
			order = append(order, i)
		}
	}
	for n, i := range order {
		wait := time.Duration(0)
		if n == 0 {
			wait = allWait
		}
		img, fresh, err := a.screens[i].capture(wait)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		got++
		pics[i] = img
		if fresh {
			a.gens[i]++
			if n > 0 {
				a.last = i
			}
		}
	}
	if got == 0 {
		return nil, firstErr
	}
	a.turn ^= 1
	dst := a.imgs[a.turn]
	for i, img := range pics {
		if img == nil || a.drew[a.turn][i] == a.gens[i] && a.gens[i] != 0 {
			continue
		}
		r := a.displays[i].Sub(a.union.Min)
		draw.Draw(dst, r, img, img.Bounds().Min, draw.Src)
		a.drew[a.turn][i] = a.gens[i]
	}
	return dst, nil
}

func (a *allScreens) Close() {
	for _, s := range a.screens {
		s.close()
	}
}

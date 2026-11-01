// Package capture provides screen image sources for the host agent.
package capture

import (
	"errors"
	"image"
	"image/color"
	"image/draw"
	"log"
	"sync"
	"time"

	"github.com/kbinani/screenshot"

	"github.com/aleynikovich/remoter/internal/protocol"
)

// Source produces screen images. A source may draw into an image again
// on the second Capture call after the one that returned it, so a caller
// can work on one capture while taking the next.
type Source interface {
	Capture() (image.Image, error)
}

// Screen captures a physical display, or every display at once (All). It
// uses a faster platform-specific capturer where there is one and falls
// back to a generic one.
type Screen struct {
	// Overlay, if set, draws on each capture (the mouse pointer). origin
	// is where the image's top-left pixel is on the desktop.
	Overlay func(img image.Image, origin image.Point) image.Image

	mu      sync.Mutex
	display int
	region  *protocol.Region // the part of the display shown, nil for all of it
	crop    *image.RGBA      // reused when a capture has to be copied to crop it
	native  nativeSource
	all     *allScreens
	started bool
	logged  bool
}

type nativeSource interface {
	Capture() (image.Image, error)
	Close()
}

// NewScreen captures the display with the given index, or All.
func NewScreen(display int) *Screen {
	return &Screen{display: display}
}

// Display returns the index of the display being captured, or All.
func (s *Screen) Display() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.display
}

// SetDisplay switches to another display, or All, from the next capture on.
func (s *Screen) SetDisplay(display int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if display == s.display {
		return
	}
	s.display = display
	s.closeLocked()
}

// Region returns the part of the display being captured, or nil for all
// of it.
func (s *Screen) Region() *protocol.Region {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.region
}

// SetRegion captures only part of the display (or of every display, for
// All) from the next capture on: a virtual monitor. nil means all of it.
func (s *Screen) SetRegion(r *protocol.Region) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.region = r.Clean()
}

func (s *Screen) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closeLocked()
}

func (s *Screen) closeLocked() {
	if s.native != nil {
		s.native.Close()
	}
	if s.all != nil {
		s.all.Close()
	}
	s.native, s.all, s.started = nil, nil, false
}

// Capture grabs the display, or the region of it set with SetRegion.
func (s *Screen) Capture() (image.Image, error) {
	img, _, err := s.capture(-1)
	if err != nil {
		return img, err
	}
	origin := img.Bounds().Min
	s.mu.Lock()
	if s.display == All && s.all != nil {
		origin = s.all.union.Min
	}
	region := s.region
	s.mu.Unlock()
	if region != nil {
		var off image.Point
		img, off = s.cropTo(img, region)
		origin = origin.Add(off)
	}
	if s.Overlay == nil {
		return img, nil
	}
	return s.Overlay(img, origin), nil
}

// cropTo returns the region of img, and how far its top-left pixel is from
// img's. An RGBA capture is cropped without copying.
func (s *Screen) cropTo(img image.Image, region *protocol.Region) (image.Image, image.Point) {
	b := img.Bounds()
	r := cropRect(b, region)
	if r.Empty() || r == b {
		return img, image.Point{}
	}
	if rgba, ok := img.(*image.RGBA); ok {
		return rgba.SubImage(r), r.Min.Sub(b.Min)
	}
	if s.crop == nil || s.crop.Rect != r {
		s.crop = image.NewRGBA(r)
	}
	draw.Draw(s.crop, r, img, r.Min, draw.Src)
	return s.crop, r.Min.Sub(b.Min)
}

// cropRect is the region of b, at least 16 pixels each way and with an even
// width and height, which video encoders need.
func cropRect(b image.Rectangle, region *protocol.Region) image.Rectangle {
	r := region.Of(b)
	if r.Dx() < 16 || r.Dy() < 16 {
		return b
	}
	r.Max.X -= r.Dx() % 2
	r.Max.Y -= r.Dy() % 2
	return r
}

// capture grabs the display. A capturer that can tell waits up to wait for
// the picture to change (a negative wait means its usual wait) and says
// whether it did; others always report a fresh picture.
func (s *Screen) capture(wait time.Duration) (image.Image, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.display == All {
		if s.all != nil && s.all.stale() {
			s.closeLocked()
		}
		if s.all == nil {
			ds := Displays()
			if len(ds) == 0 {
				return nil, false, errors.New("no displays")
			}
			s.all = newAllScreens(ds)
		}
		img, err := s.all.Capture()
		return img, true, err
	}
	if !s.started {
		s.started = true
		s.native = newNative(s.display)
	}
	if s.native != nil {
		var img image.Image
		var err error
		fresh := true
		if w, ok := s.native.(waitCapturer); ok && wait >= 0 {
			img, fresh, err = w.CaptureWait(wait)
		} else {
			img, err = s.native.Capture()
		}
		if err == nil {
			s.logged = false
			return img, fresh, nil
		}
		if !s.logged {
			s.logged = true
			log.Printf("fast capture failed (%v), using fallback", err)
		}
	}
	if screenshot.NumActiveDisplays() <= s.display {
		return nil, false, errors.New("display not found")
	}
	img, err := screenshot.CaptureDisplay(s.display)
	return img, true, err
}

// TestPattern draws a moving pattern. It needs no display, which makes it
// useful for headless machines and tests.
type TestPattern struct {
	Width, Height int
}

// Capture renders the pattern for the current time.
func (t TestPattern) Capture() (image.Image, error) {
	w, h := t.Width, t.Height
	if w == 0 || h == 0 {
		w, h = 1280, 720
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	bars := []color.RGBA{
		{192, 192, 192, 255}, {192, 192, 0, 255}, {0, 192, 192, 255}, {0, 192, 0, 255},
		{192, 0, 192, 255}, {192, 0, 0, 255}, {0, 0, 192, 255},
	}
	for i, c := range bars {
		r := image.Rect(i*w/len(bars), 0, (i+1)*w/len(bars), h)
		draw.Draw(img, r, &image.Uniform{c}, image.Point{}, draw.Src)
	}
	// A white block sweeps across so motion is visible in the viewer.
	period := 4 * time.Second
	phase := float64(time.Now().UnixNano()%int64(period)) / float64(period)
	x := int(phase * float64(w-h/8))
	draw.Draw(img, image.Rect(x, h/2-h/16, x+h/8, h/2+h/16), image.White, image.Point{}, draw.Src)
	return img, nil
}

// screenshotDisplays lists the monitors the generic capturer sees.
func screenshotDisplays() []image.Rectangle {
	var rs []image.Rectangle
	for i := range screenshot.NumActiveDisplays() {
		rs = append(rs, screenshot.GetDisplayBounds(i))
	}
	return rs
}

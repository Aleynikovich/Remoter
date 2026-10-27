// Package capture provides screen image sources for the host agent.
package capture

import (
	"errors"
	"image"
	"image/color"
	"image/draw"
	"time"

	"github.com/kbinani/screenshot"
)

// Source produces screen images.
type Source interface {
	Capture() (image.Image, error)
}

// Screen captures a physical display.
type Screen struct {
	Display int
}

// Capture grabs the whole display.
func (s Screen) Capture() (image.Image, error) {
	if screenshot.NumActiveDisplays() <= s.Display {
		return nil, errors.New("display not found")
	}
	return screenshot.CaptureDisplay(s.Display)
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

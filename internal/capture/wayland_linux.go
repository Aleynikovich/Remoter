//go:build linux

package capture

import (
	"errors"
	"image"
	"os"

	"github.com/aleynikovich/remoter/internal/wayland"
)

// newNative captures with wlr-screencopy on Hyprland, Sway and other
// wlroots compositors. X11 sessions use the generic capturer.
func newNative(display int) nativeSource {
	if !wayland.Available() && os.Getenv("DISPLAY") != "" {
		return nil
	}
	return &waylandSource{display: display}
}

type waylandSource struct {
	display int
	conn    *wayland.Conn
	copier  *wayland.Copier
}

func (s *waylandSource) Capture() (image.Image, error) {
	c, err := wayland.Shared()
	if err != nil {
		return nil, err
	}
	if c != s.conn || s.copier == nil {
		s.Close()
		outs := c.Outputs()
		if s.display >= len(outs) {
			return nil, errors.New("display not found")
		}
		if s.copier, err = c.NewCopier(outs[s.display]); err != nil {
			return nil, err
		}
		s.conn = c
	}
	img, err := s.copier.Capture()
	if err != nil {
		s.Close()
		return nil, err
	}
	return img, nil
}

func (s *waylandSource) Close() {
	if s.copier != nil {
		s.copier.Close()
		s.copier = nil
	}
}

// Displays lists the bounds of each monitor, in the coordinates input uses.
func Displays() []image.Rectangle {
	if wayland.Available() {
		if c, err := wayland.Shared(); err == nil {
			var rs []image.Rectangle
			for _, o := range c.Outputs() {
				rs = append(rs, image.Rect(o.X, o.Y, o.X+o.W, o.Y+o.H))
			}
			return rs
		}
	}
	return screenshotDisplays()
}

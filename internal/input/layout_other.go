//go:build !linux

package input

import "image"

func newLayout(screens func() []image.Rectangle) (Layout, error) {
	return newBoxLayout(screens)
}

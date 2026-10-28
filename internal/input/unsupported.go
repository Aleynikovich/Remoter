//go:build !linux && !(windows && (amd64 || arm64))

package input

import (
	"errors"
	"image"
)

func newNative(image.Rectangle) (Handler, error) {
	return nil, errors.New("remote control is not implemented on this platform yet")
}

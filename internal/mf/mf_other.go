//go:build !windows

package mf

import (
	"errors"
	"image"
)

var errUnsupported = errors.New("Media Foundation is only on Windows")

// Encoder is an H.264 encoder backed by a Media Foundation transform.
type Encoder struct{}

// NewEncoder fails: there is no Media Foundation here.
func NewEncoder(w, h, kbps int) (*Encoder, error) { return nil, errUnsupported }

func (e *Encoder) Name() string     { return "" }
func (e *Encoder) Size() (w, h int) { return 0, 0 }
func (e *Encoder) Close()           {}
func (e *Encoder) Encode(img *image.RGBA, key bool) ([]byte, bool, error) {
	return nil, false, errUnsupported
}

// SelfTest fails: there is no Media Foundation here.
func SelfTest(sizes ...image.Point) (string, error) { return "", errUnsupported }

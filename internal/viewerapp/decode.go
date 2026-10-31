//go:build linux || windows

package viewerapp

import (
	"errors"
	"unsafe"

	"github.com/aleynikovich/remoter/internal/h264"
)

// videoDecoder turns the host's H.264 access units into YUV pictures. Each
// session gets a new one, since the stream restarts with a key frame.
type videoDecoder interface {
	// decode feeds one access unit and reports whether it completed a
	// picture, which picture then describes until the next call.
	decode(au []byte) (bool, error)
	picture() (yuvPicture, error)
	close()
}

// yuvPicture points at the planes of a decoded 4:2:0 picture.
type yuvPicture struct {
	w, h    int
	planes  [3]unsafe.Pointer
	pitches [3]int32
}

// openH264Only is set when OpenH264 is the decoder. It handles only some
// of the H.264 profiles phones encode, so phones are asked for Baseline.
var openH264Only bool

// openH264Decoder decodes with Cisco's OpenH264, the library the host
// encodes with.
type openH264Decoder struct {
	d    *h264.Decoder
	last *h264.Picture
}

func newOpenH264Decoder() (videoDecoder, error) {
	d, err := h264.NewDecoder()
	if err != nil {
		return nil, err
	}
	return &openH264Decoder{d: d}, nil
}

func (o *openH264Decoder) decode(au []byte) (bool, error) {
	p, err := o.d.Decode(au)
	if err != nil {
		// A failed frame may have overwritten the last picture's buffers.
		o.last = nil
		return false, err
	}
	if p != nil {
		o.last = p
	}
	return p != nil, nil
}

func (o *openH264Decoder) picture() (yuvPicture, error) {
	p := o.last
	if p == nil {
		return yuvPicture{}, errors.New("no picture")
	}
	return yuvPicture{
		w: p.Width, h: p.Height, planes: p.Planes,
		pitches: [3]int32{int32(p.Strides[0]), int32(p.Strides[1]), int32(p.Strides[2])},
	}, nil
}

func (o *openH264Decoder) close() { o.d.Close() }

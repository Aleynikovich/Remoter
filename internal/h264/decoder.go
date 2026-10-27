package h264

import (
	"errors"
	"unsafe"

	"github.com/ebitengine/purego"
)

// Decoder layouts follow OpenH264 2.4.1's codec_api.h, like the encoder's.
const (
	decParamSize   = 32 // SDecodingParam
	bufferInfoSize = 72 // SBufferInfo

	// ISVCDecoder vtable slots.
	vDecInitialize         = 0
	vDecUninitialize       = 1
	vDecDecodeFrameNoDelay = 3
)

// Decoder turns an H.264 stream back into YUV 4:2:0 pictures. It is meant
// for the stream Encoder produces: constrained baseline, no B-frames, so
// every access unit that completes a picture returns it at once.
type Decoder struct {
	dec  unsafe.Pointer // ISVCDecoder*
	fns  *[10]uintptr
	info [bufferInfoSize]byte
	dst  [3]unsafe.Pointer
}

// Picture points at the planes of a decoded picture. The memory belongs to
// the decoder and stays valid until the next Decode.
type Picture struct {
	Width, Height int
	Planes        [3]unsafe.Pointer // Y, U, V
	Strides       [3]int
}

// NewDecoder returns a decoder. Load must have succeeded.
func NewDecoder() (*Decoder, error) {
	if !ready.Load() {
		return nil, errors.New("h264: OpenH264 is not loaded")
	}
	if createDecoder == 0 || destroyDecoder == 0 {
		return nil, errors.New("h264: this OpenH264 has no decoder")
	}
	d := &Decoder{}
	if r, _, _ := purego.SyscallN(createDecoder, uintptr(unsafe.Pointer(&d.dec))); r != 0 || d.dec == nil {
		return nil, errors.New("h264: WelsCreateDecoder failed")
	}
	d.fns = (*[10]uintptr)(*(*unsafe.Pointer)(d.dec))
	// All zero: no output file, error concealment off (a broken frame is
	// reported, and the caller asks for a key frame), AVC bitstream.
	var param [decParamSize]byte
	if r := d.call(vDecInitialize, uintptr(unsafe.Pointer(&param))); r != 0 {
		purego.SyscallN(destroyDecoder, uintptr(d.dec))
		return nil, errors.New("h264: decoder Initialize failed")
	}
	return d, nil
}

// Decode feeds one access unit. It returns the picture it completed, or
// nil when it needs more data.
func (d *Decoder) Decode(au []byte) (*Picture, error) {
	if len(au) == 0 {
		return nil, nil
	}
	clear(d.info[:])
	d.dst = [3]unsafe.Pointer{}
	state := d.call(vDecDecodeFrameNoDelay, uintptr(unsafe.Pointer(&au[0])), uintptr(len(au)),
		uintptr(unsafe.Pointer(&d.dst)), uintptr(unsafe.Pointer(&d.info)))
	if state != 0 {
		return nil, errors.New("h264: frame could not be decoded")
	}
	if *(*int32)(unsafe.Pointer(&d.info[0])) != 1 { // iBufferStatus
		return nil, nil
	}
	i32 := func(off int) int { return int(*(*int32)(unsafe.Pointer(&d.info[off]))) }
	return &Picture{
		Width:   i32(24),
		Height:  i32(28),
		Planes:  d.dst,
		Strides: [3]int{i32(36), i32(40), i32(40)},
	}, nil
}

// Close frees the decoder.
func (d *Decoder) Close() {
	if d.dec == nil {
		return
	}
	d.call(vDecUninitialize)
	purego.SyscallN(destroyDecoder, uintptr(d.dec))
	d.dec = nil
}

func (d *Decoder) call(slot int, args ...uintptr) uintptr {
	r, _, _ := purego.SyscallN(d.fns[slot], append([]uintptr{uintptr(d.dec)}, args...)...)
	return uintptr(int32(r))
}

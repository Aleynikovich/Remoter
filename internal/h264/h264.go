// Package h264 encodes screen captures as H.264 video with Cisco's OpenH264
// library, loaded at run time so the program needs no cgo. Browsers decode the
// stream with WebCodecs, which takes far less bandwidth than JPEG images and
// keeps up with window drags and scrolling.
package h264

import (
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"math"
	"runtime"
	"sync"
	"sync/atomic"
	"unsafe"

	"github.com/ebitengine/purego"
)

// Library calls and layouts follow OpenH264 2.4.1's codec_api.h. Structs are
// kept as byte buffers written at the C offsets, which are the same on every
// 64-bit platform.
const (
	usageScreenRealTime = 1 // SCREEN_CONTENT_REAL_TIME
	rcBitrate           = 1 // RC_BITRATE_MODE
	formatI420          = 23
	frameSkip           = 4 // videoFrameTypeSkip
	frameIDR            = 1
	optDataFormat       = 0
	optTraceLevel       = 25
	traceQuiet          = 0
	sliceFixed          = 1 // SM_FIXEDSLCNUM_SLICE

	paramExtSize  = 920
	sourceSize    = 72
	frameInfoSize = 5144
	layerInfoSize = 40
	maxLayers     = 128

	// ISVCEncoder vtable slots.
	vInitializeExt    = 1
	vGetDefaultParams = 2
	vUninitialize     = 3
	vEncodeFrame      = 4
	vForceIntraFrame  = 6
	vSetOption        = 7
)

var (
	loadOnce sync.Once
	loadErr  error
	ready    atomic.Bool
	create   uintptr // WelsCreateSVCEncoder
	destroy  uintptr // WelsDestroySVCEncoder
)

// Load loads the OpenH264 library. It is safe to call more than once; later
// calls return the first result.
func Load() error {
	loadOnce.Do(func() {
		if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
			loadErr = errors.New("h264: needs a 64-bit platform")
			return
		}
		create, destroy, loadErr = openLibrary()
		ready.Store(loadErr == nil)
	})
	return loadErr
}

// Ready reports whether Load has finished successfully, without waiting
// for it.
func Ready() bool { return ready.Load() }

// Encoder turns screen images of a fixed size into H.264 frames.
type Encoder struct {
	enc  unsafe.Pointer // ISVCEncoder*
	fns  *[10]uintptr
	w, h int

	yuv  []byte
	src  []byte
	info []byte
	ts   int64
}

// NewEncoder starts an encoder for w×h images (rounded down to even sizes)
// at the given target bit rate in kbit/s and frame rate.
func NewEncoder(w, h, kbps, fps int) (*Encoder, error) {
	if err := Load(); err != nil {
		return nil, err
	}
	w, h = w&^1, h&^1
	if w < 16 || h < 16 {
		return nil, errors.New("h264: picture too small")
	}
	e := &Encoder{w: w, h: h, info: make([]byte, frameInfoSize), src: make([]byte, sourceSize)}
	if r, _, _ := purego.SyscallN(create, uintptr(unsafe.Pointer(&e.enc))); r != 0 || e.enc == nil {
		return nil, fmt.Errorf("h264: WelsCreateSVCEncoder failed (%d)", r)
	}
	e.fns = (*[10]uintptr)(*(*unsafe.Pointer)(e.enc))

	p := make([]byte, paramExtSize)
	if r := e.call(vGetDefaultParams, uintptr(unsafe.Pointer(&p[0]))); r != 0 {
		e.Close()
		return nil, fmt.Errorf("h264: GetDefaultParams failed (%d)", r)
	}
	le := binary.LittleEndian
	threads := min(max(runtime.NumCPU()/2, 1), 4)
	bps := uint32(kbps * 1000)
	le.PutUint32(p[0:], usageScreenRealTime)
	le.PutUint32(p[4:], uint32(w))
	le.PutUint32(p[8:], uint32(h))
	le.PutUint32(p[12:], bps)
	le.PutUint32(p[16:], rcBitrate)
	le.PutUint32(p[20:], math.Float32bits(float32(fps)))
	le.PutUint32(p[24:], 1)                // iTemporalLayerNum
	le.PutUint32(p[28:], 1)                // iSpatialLayerNum
	le.PutUint32(p[836:], 0)               // uiIntraPeriod: key frames only on request
	p[860] = 1                             // bEnableFrameSkip, so a busy link drops frames, not latency
	p[909] = 0                             // bEnableBackgroundDetection and
	p[910] = 0                             // bEnableAdaptiveQuant are not supported for screen content
	le.PutUint32(p[864:], bps*2)           // iMaxBitrate
	le.PutUint16(p[892:], uint16(threads)) // iMultipleThreadIdc
	layer := p[32:]                        // sSpatialLayers[0]
	le.PutUint32(layer[0:], uint32(w))     // iVideoWidth
	le.PutUint32(layer[4:], uint32(h))     // iVideoHeight
	le.PutUint32(layer[8:], math.Float32bits(float32(fps)))
	le.PutUint32(layer[12:], bps)             // iSpatialBitrate
	le.PutUint32(layer[16:], bps*2)           // iMaxSpatialBitrate
	le.PutUint32(layer[32:], sliceFixed)      // uiSliceMode
	le.PutUint32(layer[36:], uint32(threads)) // uiSliceNum
	// Say the colours are BT.601 limited range, which toI420 produces;
	// otherwise browsers assume BT.709 for HD sizes and tint the picture.
	layer[184] = 1 // bVideoSignalTypePresent
	layer[185] = 5 // uiVideoFormat: unspecified
	layer[186] = 0 // bFullRange
	layer[187] = 1 // bColorDescriptionPresent
	layer[188] = 6 // uiColorPrimaries: SMPTE 170M
	layer[189] = 6 // uiTransferCharacteristics: SMPTE 170M
	layer[190] = 6 // uiColorMatrix: SMPTE 170M
	if r := e.call(vInitializeExt, uintptr(unsafe.Pointer(&p[0]))); r != 0 {
		e.Close()
		return nil, fmt.Errorf("h264: InitializeExt failed (%d)", r)
	}
	runtime.KeepAlive(p)
	level := int32(traceQuiet)
	e.call(vSetOption, optTraceLevel, uintptr(unsafe.Pointer(&level)))
	format := int32(formatI420)
	e.call(vSetOption, optDataFormat, uintptr(unsafe.Pointer(&format)))

	e.yuv = make([]byte, w*h*3/2)
	le.PutUint32(e.src[0:], formatI420)
	le.PutUint32(e.src[4:], uint32(w))
	le.PutUint32(e.src[8:], uint32(w/2))
	le.PutUint32(e.src[12:], uint32(w/2))
	le.PutUint32(e.src[56:], uint32(w))
	le.PutUint32(e.src[60:], uint32(h))
	planes := (*[4]unsafe.Pointer)(unsafe.Pointer(&e.src[24]))
	planes[0] = unsafe.Pointer(&e.yuv[0])
	planes[1] = unsafe.Pointer(&e.yuv[w*h])
	planes[2] = unsafe.Pointer(&e.yuv[w*h+w*h/4])
	return e, nil
}

// Size returns the encoded picture size.
func (e *Encoder) Size() (w, h int) { return e.w, e.h }

// Encode compresses one image, which must be at least the encoder's size. It
// returns the Annex B bytes of the frame and whether it is a key frame. When
// the rate control drops the frame, data is nil.
func (e *Encoder) Encode(img *image.RGBA, key bool) (data []byte, isKey bool, err error) {
	if key {
		e.call(vForceIntraFrame, 1)
	}
	toI420(e.yuv, img, e.w, e.h)
	e.ts += 33
	binary.LittleEndian.PutUint64(e.src[64:], uint64(e.ts))
	if r := e.call(vEncodeFrame, uintptr(unsafe.Pointer(&e.src[0])), uintptr(unsafe.Pointer(&e.info[0]))); r != 0 {
		return nil, false, fmt.Errorf("h264: EncodeFrame failed (%d)", r)
	}
	runtime.KeepAlive(e.yuv)
	le := binary.LittleEndian
	typ := le.Uint32(e.info[5128:])
	if typ == frameSkip {
		return nil, false, nil
	}
	layers := int(le.Uint32(e.info[0:]))
	for i := range min(layers, maxLayers) {
		l := e.info[8+i*layerInfoSize:]
		nals := int(le.Uint32(l[16:]))
		lens := *(*unsafe.Pointer)(unsafe.Pointer(&l[24]))
		buf := *(*unsafe.Pointer)(unsafe.Pointer(&l[32]))
		if nals <= 0 || lens == nil || buf == nil {
			continue
		}
		size := 0
		for _, n := range unsafe.Slice((*int32)(lens), nals) {
			size += int(n)
		}
		data = append(data, unsafe.Slice((*byte)(buf), size)...)
	}
	return data, typ == frameIDR, nil
}

// Close frees the encoder.
func (e *Encoder) Close() {
	if e.enc == nil {
		return
	}
	e.call(vUninitialize)
	purego.SyscallN(destroy, uintptr(e.enc))
	e.enc = nil
}

func (e *Encoder) call(slot int, args ...uintptr) uintptr {
	r, _, _ := purego.SyscallN(e.fns[slot], append([]uintptr{uintptr(e.enc)}, args...)...)
	return uintptr(int32(r))
}

// toI420 converts the top-left w×h pixels of img to planar YUV 4:2:0 with
// BT.601 limited-range coefficients, which is what decoders assume when the
// stream does not say otherwise. Rows are split across CPUs.
func toI420(dst []byte, img *image.RGBA, w, h int) {
	yp, up, vp := dst[:w*h], dst[w*h:w*h+w*h/4], dst[w*h+w*h/4:]
	workers := min(runtime.GOMAXPROCS(0), 8)
	rowsPer := (h/2 + workers - 1) / workers * 2
	var wg sync.WaitGroup
	for y0 := 0; y0 < h; y0 += rowsPer {
		wg.Add(1)
		go func(y0, y1 int) {
			defer wg.Done()
			for y := y0; y < y1; y += 2 {
				r0 := img.Pix[y*img.Stride : y*img.Stride+w*4]
				r1 := img.Pix[(y+1)*img.Stride : (y+1)*img.Stride+w*4]
				y0row := yp[y*w : y*w+w]
				y1row := yp[(y+1)*w : (y+1)*w+w]
				c := y / 2 * (w / 2)
				ur, vr := up[c:c+w/2], vp[c:c+w/2]
				for x := 0; x+1 < w; x += 2 {
					i := x * 4
					a, b := r0[i:i+8:i+8], r1[i:i+8:i+8]
					ra, ga, ba := int32(a[0]), int32(a[1]), int32(a[2])
					rb, gb, bb := int32(a[4]), int32(a[5]), int32(a[6])
					rc, gc, bc := int32(b[0]), int32(b[1]), int32(b[2])
					rd, gd, bd := int32(b[4]), int32(b[5]), int32(b[6])
					y0row[x] = uint8((66*ra+129*ga+25*ba+128)>>8 + 16)
					y0row[x+1] = uint8((66*rb+129*gb+25*bb+128)>>8 + 16)
					y1row[x] = uint8((66*rc+129*gc+25*bc+128)>>8 + 16)
					y1row[x+1] = uint8((66*rd+129*gd+25*bd+128)>>8 + 16)
					sr, sg, sb := (ra+rb+rc+rd)>>2, (ga+gb+gc+gd)>>2, (ba+bb+bc+bd)>>2
					ur[x/2] = uint8((-38*sr-74*sg+112*sb+128)>>8 + 128)
					vr[x/2] = uint8((112*sr-94*sg-18*sb+128)>>8 + 128)
				}
			}
		}(y0, min(y0+rowsPer, h))
	}
	wg.Wait()
}

// SelfTest encodes a few small frames and checks the output looks like
// H.264. Run it in a child process before trusting the library: a library
// that does not match this binding would crash the whole program.
func SelfTest() error {
	e, err := NewEncoder(320, 240, 500, 30)
	if err != nil {
		return err
	}
	defer e.Close()
	img := image.NewRGBA(image.Rect(0, 0, 320, 240))
	for i := range 3 {
		for p := range img.Pix {
			img.Pix[p] = byte(p*7 + i*13)
		}
		data, key, err := e.Encode(img, i == 0)
		if err != nil {
			return err
		}
		if i == 0 && (!key || len(data) < 4 || data[0] != 0 || data[1] != 0) {
			return errors.New("h264: first frame is not an Annex B key frame")
		}
	}
	return nil
}

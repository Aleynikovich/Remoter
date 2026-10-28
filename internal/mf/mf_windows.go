//go:build windows

package mf

import (
	"errors"
	"fmt"
	"image"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

// Calls and vtable layouts follow the Windows SDK's mfapi.h, mftransform.h,
// mfobjects.h and strmif.h (ICodecAPI).

var (
	mfplat           = syscall.NewLazyDLL("mfplat.dll")
	ole32            = syscall.NewLazyDLL("ole32.dll")
	procMFStartup    = mfplat.NewProc("MFStartup")
	procMFTEnumEx    = mfplat.NewProc("MFTEnumEx")
	procMediaType    = mfplat.NewProc("MFCreateMediaType")
	procCreateSample = mfplat.NewProc("MFCreateSample")
	procMemoryBuffer = mfplat.NewProc("MFCreateMemoryBuffer")
	procCoInit       = ole32.NewProc("CoInitializeEx")
	procCoTaskFree   = ole32.NewProc("CoTaskMemFree")
)

type guid [16]byte

// mustGUID parses "xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx" into the in-memory
// layout of a GUID.
func mustGUID(s string) *guid {
	p := strings.Split(s, "-")
	var g guid
	parse := func(s string, bits int) uint64 {
		v, err := strconv.ParseUint(s, 16, bits)
		if err != nil {
			panic(err)
		}
		return v
	}
	a, b, c := parse(p[0], 32), parse(p[1], 16), parse(p[2], 16)
	g[0], g[1], g[2], g[3] = byte(a), byte(a>>8), byte(a>>16), byte(a>>24)
	g[4], g[5], g[6], g[7] = byte(b), byte(b>>8), byte(c), byte(c>>8)
	rest := p[3] + p[4]
	for i := range 8 {
		g[8+i] = byte(parse(rest[i*2:i*2+2], 8))
	}
	return &g
}

var (
	categoryVideoEncoder = mustGUID("f79eac7d-e545-4387-bdee-d647d7bde42a")
	mediaTypeVideo       = mustGUID("73646976-0000-0010-8000-00aa00389b71")
	formatNV12           = mustGUID("3231564e-0000-0010-8000-00aa00389b71")
	formatH264           = mustGUID("34363248-0000-0010-8000-00aa00389b71")

	mtMajorType      = mustGUID("48eba18e-f8c9-4687-bf11-0a74c9f96a8f")
	mtSubtype        = mustGUID("f7e34c9a-42e8-4714-b74b-cb29d72c35e5")
	mtAvgBitrate     = mustGUID("20332624-fb0d-4d9e-bd0d-cbf6786c102e")
	mtFrameSize      = mustGUID("1652c33d-d6b2-4012-b834-72030849a37d")
	mtFrameRate      = mustGUID("c459a2e8-3d2c-4e44-b132-fee5156c7bb0")
	mtPixelAspect    = mustGUID("c6376a1e-8d0a-4027-be45-6d9a0ad39bb6")
	mtInterlaceMode  = mustGUID("e2724bb8-e676-4806-b4b2-a8d6efb44ccd")
	mtProfile        = mustGUID("ad76a80b-2d5c-4e0b-b375-64e520137036")
	mtSequenceHeader = mustGUID("3c036de7-3ad0-4c9e-9216-ee6d6ac21cb3")
	mtYUVMatrix      = mustGUID("3e23d450-2c75-4d25-a00e-b91670d12327")
	mtNominalRange   = mustGUID("c21b8ee5-b956-4071-8daf-325edf5cab11")
	mtPrimaries      = mustGUID("dbfbe4d7-0740-4ee0-8192-850ab0e21935")
	mtTransfer       = mustGUID("5fb0fce9-be5c-4935-a811-ec838f8eed93")

	transformAsync       = mustGUID("f81a699a-649a-497d-8c73-29f8fed6ad7a")
	transformAsyncUnlock = mustGUID("e5666d6b-3422-4eb6-a421-da7db1f8e207")
	lowLatency           = mustGUID("9c27891a-ed7a-40e1-88e8-b22727a024ee") // also CODECAPI_AVLowLatencyMode
	friendlyName         = mustGUID("314ffbae-5b41-4c95-9c19-4e7d586face3")

	iidTransform      = mustGUID("bf94c121-5b05-4e6f-8000-ba598961414d")
	iidEventGenerator = mustGUID("2cd0bd52-bcd5-4b89-b62c-eadc0c031e7d")
	iidCodecAPI       = mustGUID("901db4c7-31ce-41a2-85dc-8fa0bf41b8da")

	apiRateControl = mustGUID("1c0608e9-370c-4710-8a58-cb6181c42423")
	apiMeanBitrate = mustGUID("f7222374-2144-4815-b550-a37f8e12ee52")
	apiBFrames     = mustGUID("8d390aac-dc5c-4200-b57f-814d04babab2")
	apiForceKey    = mustGUID("398c1b98-8353-475a-9ef2-8f265d260345")
	apiGOPSize     = mustGUID("95f31b26-95a4-41aa-9303-246a7fc6eef1")
)

// vtable slots.
const (
	vQueryInterface = 0
	vRelease        = 2

	// IMFAttributes, which media types, samples and events extend.
	vGetUINT32           = 7
	vGetGUID             = 10
	vGetBlobSize         = 14
	vGetBlob             = 15
	vGetAllocatedString  = 13
	vSetUINT32           = 21
	vSetUINT64           = 22
	vSetGUID             = 24
	vActivateObject      = 33 // IMFActivate
	vShutdownObject      = 34 //
	vSetSampleTime       = 36 // IMFSample
	vSetSampleDuration   = 38 //
	vConvertToContiguous = 41 //
	vAddBuffer           = 42 //
	vEventGetType        = 33 // IMFMediaEvent

	vBufferLock             = 3 // IMFMediaBuffer
	vBufferUnlock           = 4
	vBufferSetCurrentLength = 6

	vGetOutputStreamInfo    = 7 // IMFTransform
	vGetAttributes          = 8
	vGetInputAvailableType  = 13
	vGetOutputAvailableType = 14
	vSetInputType           = 15
	vSetOutputType          = 16
	vGetOutputCurrentType   = 18
	vProcessMessage         = 23
	vProcessInput           = 24
	vProcessOutput          = 25

	vGetEvent = 3 // IMFMediaEventGenerator
	vSetValue = 9 // ICodecAPI
)

const (
	mfVersion        = 0x00020070
	coinitMultithrd  = 0
	enumSyncMFT      = 0x1
	enumHardware     = 0x4
	enumSortAndFilt  = 0x40
	eventNoWait      = 1
	needInput        = 601 // METransformNeedInput
	haveOutput       = 602 // METransformHaveOutput
	notifyBeginStrm  = 0x10000000
	notifyEndStrm    = 0x10000001
	notifyStartOfStr = 0x10000003
	commandFlush     = 0
	providesSamples  = 0x100 | 0x200 // MFT_OUTPUT_STREAM_(CAN_)PROVIDES_SAMPLES
	progressive      = 2
	profileBaseline  = 66
	profileMain      = 77
	matrixBT709      = 1
	primariesBT709   = 2
	transferBT709    = 5
	range16to235     = 2
	rateLowDelayVBR  = 4 // eAVEncCommonRateControlMode_LowDelayVBR
	vtUI4            = 19
	variantSize      = 24
	outputBufferSize = 32 // MFT_OUTPUT_DATA_BUFFER

	hrNeedMoreInput = 0xC00D6D72
	hrStreamChange  = 0xC00D6D61
	hrNoEvents      = 0xC00D3E80
)

func comCall(obj unsafe.Pointer, slot int, args ...uintptr) uint32 {
	vtbl := *(*unsafe.Pointer)(obj)
	fn := *(*uintptr)(unsafe.Add(vtbl, slot*int(unsafe.Sizeof(uintptr(0)))))
	r, _, _ := syscall.SyscallN(fn, append([]uintptr{uintptr(obj)}, args...)...)
	return uint32(r)
}

func release(p *unsafe.Pointer) {
	if *p != nil {
		comCall(*p, vRelease)
		*p = nil
	}
}

func failed(hr uint32) bool { return hr&0x80000000 != 0 }

func g(p *guid) uintptr { return uintptr(unsafe.Pointer(p)) }

var startup = sync.OnceValue(func() error {
	if err := mfplat.Load(); err != nil {
		return fmt.Errorf("Media Foundation is not installed: %w", err)
	}
	// Any thread can then use the free-threaded encoders.
	procCoInit.Call(0, coinitMultithrd)
	if hr, _, _ := procMFStartup.Call(mfVersion, 0); failed(uint32(hr)) {
		return fmt.Errorf("MFStartup: %#x", hr)
	}
	return nil
})

// Encoder is an H.264 encoder backed by a Media Foundation transform.
type Encoder struct {
	w, h, kbps int
	flags      uintptr
	name       string

	activate unsafe.Pointer // IMFActivate
	mft      unsafe.Pointer // IMFTransform
	events   unsafe.Pointer // IMFMediaEventGenerator, for async (hardware) encoders
	codec    unsafe.Pointer // ICodecAPI, or nil
	provides bool           // the encoder allocates output samples
	outSize  uint32
	needs    int // input requests not yet answered
	haves    int // output announcements not yet collected
	start    time.Time
	frames   int
	out      []byte
}

// NewEncoder opens the first hardware H.264 encoder for w×h pictures (even
// sizes) at kbps kbit/s.
func NewEncoder(w, h, kbps int) (*Encoder, error) {
	return open(w, h, kbps, enumHardware|enumSortAndFilt)
}

func open(w, h, kbps int, flags uintptr) (*Encoder, error) {
	if err := startup(); err != nil {
		return nil, err
	}
	w, h = w&^1, h&^1
	if w < 16 || h < 16 {
		return nil, errors.New("mf: picture too small")
	}
	e := &Encoder{w: w, h: h, kbps: kbps, flags: flags, start: time.Now()}
	if err := e.init(); err != nil {
		e.Close()
		return nil, err
	}
	return e, nil
}

// Name says which encoder this is, such as "NVIDIA H.264 Encoder MFT".
func (e *Encoder) Name() string { return e.name }

// Size returns the encoded picture size.
func (e *Encoder) Size() (w, h int) { return e.w, e.h }

// init opens the first of the H.264 encoders that takes our settings.
func (e *Encoder) init() error {
	// Only the output is filtered on: some hardware encoders list their
	// input formats in ways the filter misses.
	out := [2]guid{*mediaTypeVideo, *formatH264}
	var list unsafe.Pointer
	var count uint32
	if hr, _, _ := procMFTEnumEx.Call(g(categoryVideoEncoder), e.flags, 0, uintptr(unsafe.Pointer(&out)),
		uintptr(unsafe.Pointer(&list)), uintptr(unsafe.Pointer(&count))); failed(uint32(hr)) {
		return fmt.Errorf("MFTEnumEx: %#x", hr)
	}
	if count == 0 {
		return errors.New("no H.264 encoder found")
	}
	var errs []error
	for _, a := range unsafe.Slice((*unsafe.Pointer)(list), count) {
		if e.mft != nil {
			comCall(a, vRelease)
			continue
		}
		e.activate = a
		if err := e.setup(); err != nil {
			errs = append(errs, err)
			e.Close()
		}
	}
	procCoTaskFree.Call(uintptr(list))
	if e.mft == nil {
		return errors.Join(errs...)
	}
	return nil
}

// setup activates e.activate and sets it up for our pictures.
func (e *Encoder) setup() error {
	*e = Encoder{w: e.w, h: e.h, kbps: e.kbps, flags: e.flags, start: e.start, activate: e.activate}
	var name *uint16
	var n uint32
	if !failed(comCall(e.activate, vGetAllocatedString, g(friendlyName), uintptr(unsafe.Pointer(&name)), uintptr(unsafe.Pointer(&n)))) {
		e.name = syscall.UTF16ToString(unsafe.Slice(name, n))
		procCoTaskFree.Call(uintptr(unsafe.Pointer(name)))
	}
	if hr := comCall(e.activate, vActivateObject, g(iidTransform), uintptr(unsafe.Pointer(&e.mft))); failed(hr) {
		return fmt.Errorf("%s: ActivateObject: %#x", e.name, hr)
	}

	var attrs unsafe.Pointer
	if !failed(comCall(e.mft, vGetAttributes, uintptr(unsafe.Pointer(&attrs)))) && attrs != nil {
		var async uint32
		if !failed(comCall(attrs, vGetUINT32, g(transformAsync), uintptr(unsafe.Pointer(&async)))) && async != 0 {
			if hr := comCall(attrs, vSetUINT32, g(transformAsyncUnlock), 1); failed(hr) {
				release(&attrs)
				return fmt.Errorf("%s: unlocking async encoder: %#x", e.name, hr)
			}
			if hr := comCall(e.mft, vQueryInterface, g(iidEventGenerator), uintptr(unsafe.Pointer(&e.events))); failed(hr) {
				release(&attrs)
				return fmt.Errorf("%s: no event generator: %#x", e.name, hr)
			}
		}
		comCall(attrs, vSetUINT32, g(lowLatency), 1)
		release(&attrs)
	}
	comCall(e.mft, vQueryInterface, g(iidCodecAPI), uintptr(unsafe.Pointer(&e.codec)))
	e.setValue(lowLatency, 1)
	e.setValue(apiRateControl, rateLowDelayVBR)
	e.setValue(apiMeanBitrate, uint32(e.kbps*1000))
	e.setValue(apiBFrames, 0)
	e.setValue(apiGOPSize, 0) // key frames only on request (or as the encoder sees fit)

	// Encoders want the output type first.
	var err error
	for _, profile := range []uint32{profileBaseline, profileMain} {
		if err = e.setType(vSetOutputType, formatH264, profile); err == nil {
			break
		}
	}
	if err != nil {
		return fmt.Errorf("%s: output type: %w", e.name, err)
	}
	if err := e.setType(vSetInputType, formatNV12, 0); err != nil {
		if err2 := e.setOfferedInput(); err2 != nil {
			return fmt.Errorf("%s: input type: %v, then %w", e.name, err, err2)
		}
	}

	var info [3]uint32 // MFT_OUTPUT_STREAM_INFO
	if hr := comCall(e.mft, vGetOutputStreamInfo, 0, uintptr(unsafe.Pointer(&info))); failed(hr) {
		return fmt.Errorf("%s: GetOutputStreamInfo: %#x", e.name, hr)
	}
	e.provides = info[0]&providesSamples != 0
	e.outSize = max(info[1], uint32(e.w*e.h*3/2))

	comCall(e.mft, vProcessMessage, notifyBeginStrm, 0)
	comCall(e.mft, vProcessMessage, notifyStartOfStr, 0)
	return nil
}

// setType gives the encoder a media type with format at the picture size.
func (e *Encoder) setType(slot int, format *guid, profile uint32) error {
	var mt unsafe.Pointer
	if hr, _, _ := procMediaType.Call(uintptr(unsafe.Pointer(&mt))); failed(uint32(hr)) {
		return fmt.Errorf("MFCreateMediaType: %#x", hr)
	}
	defer release(&mt)
	comCall(mt, vSetGUID, g(mtMajorType), g(mediaTypeVideo))
	comCall(mt, vSetGUID, g(mtSubtype), g(format))
	comCall(mt, vSetUINT64, g(mtFrameSize), uintptr(uint64(e.w)<<32|uint64(e.h)))
	comCall(mt, vSetUINT64, g(mtFrameRate), uintptr(uint64(60)<<32|1))
	comCall(mt, vSetUINT64, g(mtPixelAspect), uintptr(uint64(1)<<32|1))
	comCall(mt, vSetUINT32, g(mtInterlaceMode), progressive)
	comCall(mt, vSetUINT32, g(mtYUVMatrix), matrixBT709)
	comCall(mt, vSetUINT32, g(mtPrimaries), primariesBT709)
	comCall(mt, vSetUINT32, g(mtTransfer), transferBT709)
	comCall(mt, vSetUINT32, g(mtNominalRange), range16to235)
	if profile != 0 {
		comCall(mt, vSetUINT32, g(mtAvgBitrate), uintptr(e.kbps*1000))
		comCall(mt, vSetUINT32, g(mtProfile), uintptr(profile))
	}
	if hr := comCall(e.mft, slot, 0, uintptr(mt), 0); failed(hr) {
		return fmt.Errorf("%#x", hr)
	}
	return nil
}

// setOfferedInput picks the encoder's own NV12 input type, for encoders
// that refuse one built from scratch.
func (e *Encoder) setOfferedInput() error {
	for i := uintptr(0); i < 64; i++ {
		var mt unsafe.Pointer
		if failed(comCall(e.mft, vGetInputAvailableType, 0, i, uintptr(unsafe.Pointer(&mt)))) {
			break
		}
		var sub guid
		comCall(mt, vGetGUID, g(mtSubtype), uintptr(unsafe.Pointer(&sub)))
		if sub != *formatNV12 {
			release(&mt)
			continue
		}
		comCall(mt, vSetUINT64, g(mtFrameSize), uintptr(uint64(e.w)<<32|uint64(e.h)))
		comCall(mt, vSetUINT64, g(mtFrameRate), uintptr(uint64(60)<<32|1))
		hr := comCall(e.mft, vSetInputType, 0, uintptr(mt), 0)
		release(&mt)
		if failed(hr) {
			return fmt.Errorf("its own NV12 type: %#x", hr)
		}
		return nil
	}
	return errors.New("it offers no NV12 input")
}

// setValue sets an ICodecAPI property, where the encoder has one.
func (e *Encoder) setValue(prop *guid, v uint32) bool {
	if e.codec == nil {
		return false
	}
	var variant [variantSize]byte
	*(*uint16)(unsafe.Pointer(&variant[0])) = vtUI4
	*(*uint32)(unsafe.Pointer(&variant[8])) = v
	return !failed(comCall(e.codec, vSetValue, g(prop), uintptr(unsafe.Pointer(&variant[0]))))
}

// poll counts the async encoder's events until cond holds or the wait is
// over.
func (e *Encoder) poll(cond func() bool, wait time.Duration) bool {
	end := time.Now().Add(wait)
	for !cond() {
		var ev unsafe.Pointer
		switch hr := comCall(e.events, vGetEvent, eventNoWait, uintptr(unsafe.Pointer(&ev))); {
		case hr == hrNoEvents:
			if time.Now().After(end) {
				return false
			}
			time.Sleep(200 * time.Microsecond)
		case failed(hr):
			return false
		default:
			var typ uint32
			comCall(ev, vEventGetType, uintptr(unsafe.Pointer(&typ)))
			release(&ev)
			switch typ {
			case needInput:
				e.needs++
			case haveOutput:
				e.haves++
			}
		}
	}
	return true
}

// Encode compresses one image, which must be at least the encoder's size. It
// returns the Annex B bytes of the frame and whether it is a key frame; data
// is nil while the encoder holds on to the frame.
func (e *Encoder) Encode(img *image.RGBA, key bool) (data []byte, isKey bool, err error) {
	if key && e.frames > 0 && !e.setValue(apiForceKey, 1) {
		// Without ICodecAPI, a fresh encoder is the way to a key frame.
		e.Close()
		if err := e.reopen(); err != nil {
			return nil, false, err
		}
	}
	if e.events != nil && !e.poll(func() bool { return e.needs > 0 }, time.Second) {
		return nil, false, fmt.Errorf("%s: no input request", e.name)
	}
	sample, err := e.input(img)
	if err != nil {
		return nil, false, err
	}
	hr := comCall(e.mft, vProcessInput, 0, uintptr(sample), 0)
	release(&sample)
	if failed(hr) {
		return nil, false, fmt.Errorf("%s: ProcessInput: %#x", e.name, hr)
	}
	e.frames++
	if e.events != nil {
		e.needs--
		// An encoder that asks for more input before it has output keeps
		// a frame or so in its pipeline; its output comes with the next.
		t := time.Now()
		e.poll(func() bool { return e.haves > 0 || e.needs > 0 && time.Since(t) > 30*time.Millisecond }, time.Second)
		if e.haves == 0 {
			if e.needs > 0 {
				return nil, false, nil
			}
			return nil, false, fmt.Errorf("%s: no output", e.name)
		}
		e.haves--
	}
	data, err = e.output()
	if data == nil || err != nil {
		return nil, false, err
	}
	idr, sps := nalTypes(data)
	if idr && !sps {
		data = append(e.sequenceHeader(), data...)
	}
	return data, idr, nil
}

func (e *Encoder) reopen() error {
	*e = Encoder{w: e.w, h: e.h, kbps: e.kbps, flags: e.flags, start: e.start}
	return e.init()
}

// input copies img into a new sample, as an async encoder may still read
// the previous one.
func (e *Encoder) input(img *image.RGBA) (unsafe.Pointer, error) {
	size := e.w * e.h * 3 / 2
	var buf, sample unsafe.Pointer
	if hr, _, _ := procMemoryBuffer.Call(uintptr(size), uintptr(unsafe.Pointer(&buf))); failed(uint32(hr)) {
		return nil, fmt.Errorf("MFCreateMemoryBuffer: %#x", hr)
	}
	defer release(&buf)
	var p *byte
	if hr := comCall(buf, vBufferLock, uintptr(unsafe.Pointer(&p)), 0, 0); failed(hr) {
		return nil, fmt.Errorf("Lock: %#x", hr)
	}
	toNV12(unsafe.Slice(p, size), img, e.w, e.h)
	comCall(buf, vBufferUnlock)
	comCall(buf, vBufferSetCurrentLength, uintptr(size))
	if hr, _, _ := procCreateSample.Call(uintptr(unsafe.Pointer(&sample))); failed(uint32(hr)) {
		return nil, fmt.Errorf("MFCreateSample: %#x", hr)
	}
	comCall(sample, vAddBuffer, uintptr(buf))
	comCall(sample, vSetSampleTime, uintptr(time.Since(e.start)/100))
	comCall(sample, vSetSampleDuration, uintptr(time.Second/60/100))
	return sample, nil
}

// output collects one encoded frame, or nil when the encoder has none.
func (e *Encoder) output() ([]byte, error) {
	for range 2 { // a second try after the output type changed
		var ob [outputBufferSize]byte
		sample := (*unsafe.Pointer)(unsafe.Pointer(&ob[8]))
		events := (*unsafe.Pointer)(unsafe.Pointer(&ob[24]))
		if !e.provides {
			var buf unsafe.Pointer
			if hr, _, _ := procMemoryBuffer.Call(uintptr(e.outSize), uintptr(unsafe.Pointer(&buf))); failed(uint32(hr)) {
				return nil, fmt.Errorf("MFCreateMemoryBuffer: %#x", hr)
			}
			procCreateSample.Call(uintptr(unsafe.Pointer(sample)))
			comCall(*sample, vAddBuffer, uintptr(buf))
			release(&buf)
		}
		var status uint32
		hr := comCall(e.mft, vProcessOutput, 0, 1, uintptr(unsafe.Pointer(&ob[0])), uintptr(unsafe.Pointer(&status)))
		release(events)
		switch {
		case hr == hrNeedMoreInput:
			release(sample)
			return nil, nil
		case hr == hrStreamChange:
			release(sample)
			var mt unsafe.Pointer
			if hr := comCall(e.mft, vGetOutputAvailableType, 0, 0, uintptr(unsafe.Pointer(&mt))); failed(hr) {
				return nil, fmt.Errorf("%s: GetOutputAvailableType: %#x", e.name, hr)
			}
			hr := comCall(e.mft, vSetOutputType, 0, uintptr(mt), 0)
			release(&mt)
			if failed(hr) {
				return nil, fmt.Errorf("%s: SetOutputType after a change: %#x", e.name, hr)
			}
			continue
		case failed(hr):
			release(sample)
			return nil, fmt.Errorf("%s: ProcessOutput: %#x", e.name, hr)
		}
		if *sample == nil {
			return nil, nil
		}
		var buf unsafe.Pointer
		hr = comCall(*sample, vConvertToContiguous, uintptr(unsafe.Pointer(&buf)))
		release(sample)
		if failed(hr) {
			return nil, fmt.Errorf("ConvertToContiguousBuffer: %#x", hr)
		}
		var p *byte
		var n uint32
		if hr := comCall(buf, vBufferLock, uintptr(unsafe.Pointer(&p)), 0, uintptr(unsafe.Pointer(&n))); failed(hr) {
			release(&buf)
			return nil, fmt.Errorf("Lock: %#x", hr)
		}
		e.out = append(e.out[:0], unsafe.Slice(p, n)...)
		comCall(buf, vBufferUnlock)
		release(&buf)
		if len(e.out) == 0 {
			return nil, nil
		}
		return e.out, nil
	}
	return nil, fmt.Errorf("%s: output type keeps changing", e.name)
}

// sequenceHeader returns the SPS and PPS of the current output type, for
// encoders that leave them out of key frames.
func (e *Encoder) sequenceHeader() []byte {
	var mt unsafe.Pointer
	if failed(comCall(e.mft, vGetOutputCurrentType, 0, uintptr(unsafe.Pointer(&mt)))) {
		return nil
	}
	defer release(&mt)
	var n uint32
	if failed(comCall(mt, vGetBlobSize, g(mtSequenceHeader), uintptr(unsafe.Pointer(&n)))) || n == 0 {
		return nil
	}
	b := make([]byte, n)
	if failed(comCall(mt, vGetBlob, g(mtSequenceHeader), uintptr(unsafe.Pointer(&b[0])), uintptr(n), 0)) {
		return nil
	}
	return b
}

// Close frees the encoder.
func (e *Encoder) Close() {
	if e.mft != nil {
		comCall(e.mft, vProcessMessage, commandFlush, 0)
		comCall(e.mft, vProcessMessage, notifyEndStrm, 0)
	}
	release(&e.codec)
	release(&e.events)
	release(&e.mft)
	if e.activate != nil {
		comCall(e.activate, vShutdownObject)
	}
	release(&e.activate)
}

// SelfTest encodes frames with the hardware encoder at 1280×720 and at
// each of sizes, and checks the output looks like H.264. Run it in a child
// process first: a driver that does not match this binding would crash the
// whole program.
func SelfTest(sizes ...image.Point) (string, error) {
	return selfTest(enumHardware|enumSortAndFilt, sizes)
}

func selfTest(flags uintptr, sizes []image.Point) (name string, err error) {
	for _, size := range append([]image.Point{{1280, 720}}, sizes...) {
		if name, err = selfTestAt(flags, size.X, size.Y); err != nil {
			return name, fmt.Errorf("at %dx%d: %w", size.X, size.Y, err)
		}
	}
	return name, nil
}

func selfTestAt(flags uintptr, w, h int) (string, error) {
	e, err := open(w, h, 8000, flags)
	if err != nil {
		return "", err
	}
	defer e.Close()
	w, h = e.Size()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	got, keys := 0, 0
	const frames = 40
	for i := range frames {
		for p := 0; p < len(img.Pix); p += 4 {
			img.Pix[p], img.Pix[p+1], img.Pix[p+2], img.Pix[p+3] = byte(p/4%w+i*8), byte(p/4/w), byte(i*16), 255
		}
		data, key, err := e.Encode(img, i == 0 || i == 20)
		if err != nil {
			return e.name, err
		}
		if data == nil {
			continue
		}
		if len(data) < 4 || data[0] != 0 || data[1] != 0 {
			return e.name, fmt.Errorf("frame %d is not Annex B", i)
		}
		if _, sps := nalTypes(data); key && !sps {
			return e.name, errors.New("key frame without an SPS")
		}
		got++
		if key {
			keys++
		}
	}
	if got < frames-5 || keys < 2 {
		return e.name, fmt.Errorf("%d of %d frames came out, %d key frames", got, frames, keys)
	}
	return e.name, nil
}

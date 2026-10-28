//go:build windows

package capture

import (
	"errors"
	"fmt"
	"image"
	"log"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"github.com/kbinani/screenshot"
)

// windowsSource captures with DXGI Desktop Duplication, which copies the
// composed desktop on the GPU and only when it changed, and falls back to
// GDI where duplication is not available (remote desktop sessions, some
// virtual machines) or while it is being re-created.
type windowsSource struct {
	display int
	dxgi    *dxgi
	gdi     *gdi
	retry   time.Time
	logged  string
}

const (
	dxgiRetry     = 3 * time.Second
	dxgiLostRetry = 200 * time.Millisecond
)

// dxgiAllowed is set once SelfTestDXGI has passed in a child process, so a
// driver that crashes on desktop duplication cannot take the host down.
var dxgiAllowed atomic.Bool

// EnableDXGI lets captures use desktop duplication.
func EnableDXGI() { dxgiAllowed.Store(true) }

// SelfTestDXGI duplicates the first display and takes a few frames. Run it
// in a child process before calling EnableDXGI.
func SelfTestDXGI() error {
	d, err := newDXGI(0)
	if err != nil {
		return err
	}
	defer d.Close()
	for range 3 {
		img, err := d.Capture()
		if err != nil && !errors.Is(err, errNoFrame) {
			return err
		}
		if err == nil && img.Bounds() != d.bounds {
			return fmt.Errorf("frame is %v, want %v", img.Bounds(), d.bounds)
		}
		time.Sleep(20 * time.Millisecond)
	}
	return nil
}

func (s *windowsSource) Capture() (image.Image, error) {
	if s.dxgi == nil && dxgiAllowed.Load() && time.Now().After(s.retry) {
		d, err := newDXGI(s.display)
		if err != nil {
			s.retry = time.Now().Add(dxgiRetry)
			s.note(fmt.Sprintf("screen capture: GDI (desktop duplication unavailable: %v)", err))
		} else {
			s.dxgi = d
			s.note(fmt.Sprintf("screen capture: DXGI desktop duplication, %dx%d", d.bounds.Dx(), d.bounds.Dy()))
		}
	}
	if s.dxgi != nil {
		img, err := s.dxgi.Capture()
		if err == nil {
			return img, nil
		}
		if errors.Is(err, errNoFrame) {
			return s.gdi.Capture()
		}
		// Usually a mode change or the secure desktop: start over soon.
		s.dxgi.Close()
		s.dxgi = nil
		s.retry = time.Now().Add(dxgiLostRetry)
		if !errors.Is(err, errAccessLost) {
			s.retry = time.Now().Add(dxgiRetry)
			s.note(fmt.Sprintf("screen capture: GDI (desktop duplication failed: %v)", err))
		}
	}
	return s.gdi.Capture()
}

// note logs a change of capture method once.
func (s *windowsSource) note(msg string) {
	if msg != s.logged {
		s.logged = msg
		log.Print(msg)
	}
}

func (s *windowsSource) Close() {
	if s.dxgi != nil {
		s.dxgi.Close()
		s.dxgi = nil
	}
	s.gdi.Close()
}

var (
	dxgiDLL               = syscall.NewLazyDLL("dxgi.dll")
	d3d11DLL              = syscall.NewLazyDLL("d3d11.dll")
	procCreateDXGIFactory = dxgiDLL.NewProc("CreateDXGIFactory1")
	procD3D11CreateDevice = d3d11DLL.NewProc("D3D11CreateDevice")

	iidIDXGIFactory1  = guid{0x770aae78, 0xf26f, 0x4dba, [8]byte{0xa8, 0x29, 0x25, 0x3c, 0x83, 0xd1, 0xb3, 0x87}}
	iidIDXGIOutput1   = guid{0x00cddea8, 0x939b, 0x4b83, [8]byte{0xa3, 0x40, 0xa6, 0x85, 0x22, 0x66, 0x66, 0xcc}}
	iidID3D11Texture2 = guid{0x6f15aaf2, 0xd208, 0x4e89, [8]byte{0x9a, 0xb4, 0x48, 0x95, 0x35, 0xd3, 0x4f, 0x9c}}

	errAccessLost = errors.New("desktop duplication access lost")
	errNoFrame    = errors.New("desktop duplication: no frame yet")
)

type guid struct {
	a    uint32
	b, c uint16
	d    [8]byte
}

// COM vtable slots used here.
const (
	vQueryInterface = 0
	vRelease        = 2

	vFactoryEnumAdapters = 7  // IDXGIFactory
	vAdapterEnumOutputs  = 7  // IDXGIAdapter
	vOutputGetDesc       = 7  // IDXGIOutput
	vOutputDuplicate     = 22 // IDXGIOutput1::DuplicateOutput

	vDupAcquireNextFrame = 8  // IDXGIOutputDuplication
	vDupReleaseFrame     = 14 //

	vDeviceCreateTexture2D = 5  // ID3D11Device
	vContextMap            = 14 // ID3D11DeviceContext
	vContextUnmap          = 15 //
	vContextCopyResource   = 47 //

	hrWaitTimeout = 0x887A0027 // DXGI_ERROR_WAIT_TIMEOUT
	hrAccessLost  = 0x887A0026 // DXGI_ERROR_ACCESS_LOST
	hrNotFound    = 0x887A0002 // DXGI_ERROR_NOT_FOUND

	driverTypeUnknown  = 0
	sdkVersion         = 7
	formatB8G8R8A8     = 87
	usageStaging       = 3
	cpuAccessRead      = 0x20000
	mapRead            = 1
	rotationIdentity   = 1
	outputDescSize     = 96
	frameInfoSize      = 48
	texture2DDescSize  = 44
	mappedResourceSize = 16
)

// comCall calls method slot of the COM object obj and returns its HRESULT.
func comCall(obj unsafe.Pointer, slot int, args ...uintptr) uint32 {
	vtbl := *(*unsafe.Pointer)(obj)
	fn := *(*uintptr)(unsafe.Add(vtbl, slot*int(unsafe.Sizeof(uintptr(0)))))
	r, _, _ := syscall.SyscallN(fn, append([]uintptr{uintptr(obj)}, args...)...)
	return uint32(r)
}

func release(obj *unsafe.Pointer) {
	if *obj != nil {
		comCall(*obj, vRelease)
		*obj = nil
	}
}

type dxgi struct {
	bounds  image.Rectangle
	device  unsafe.Pointer // ID3D11Device
	context unsafe.Pointer // ID3D11DeviceContext
	dup     unsafe.Pointer // IDXGIOutputDuplication
	staging unsafe.Pointer // ID3D11Texture2D the CPU can read
	imgs    [2]*image.RGBA // returned in turn, see Source
	turn    int
	have    bool
}

// newDXGI starts duplicating the output that shows display.
func newDXGI(display int) (d *dxgi, err error) {
	if err := procCreateDXGIFactory.Find(); err != nil {
		return nil, err
	}
	if err := procD3D11CreateDevice.Find(); err != nil {
		return nil, err
	}
	dpiAware()
	if screenshot.NumActiveDisplays() <= display {
		return nil, errors.New("display not found")
	}
	want := screenshot.GetDisplayBounds(display)

	var factory unsafe.Pointer
	if r, _, _ := procCreateDXGIFactory.Call(uintptr(unsafe.Pointer(&iidIDXGIFactory1)), uintptr(unsafe.Pointer(&factory))); r != 0 {
		return nil, fmt.Errorf("CreateDXGIFactory1: %#x", uint32(r))
	}
	defer release(&factory)

	// Find the adapter and output whose desktop area is the display.
	var adapter, output unsafe.Pointer
	for i := 0; ; i++ {
		var a unsafe.Pointer
		if hr := comCall(factory, vFactoryEnumAdapters, uintptr(i), uintptr(unsafe.Pointer(&a))); hr != 0 {
			break
		}
		for j := 0; ; j++ {
			var o unsafe.Pointer
			if hr := comCall(a, vAdapterEnumOutputs, uintptr(j), uintptr(unsafe.Pointer(&o))); hr != 0 {
				break
			}
			var desc [outputDescSize]byte
			if comCall(o, vOutputGetDesc, uintptr(unsafe.Pointer(&desc[0]))) == 0 {
				rc := (*[4]int32)(unsafe.Pointer(&desc[64]))
				r := image.Rect(int(rc[0]), int(rc[1]), int(rc[2]), int(rc[3]))
				rotation := *(*uint32)(unsafe.Pointer(&desc[84]))
				if r == want && rotation <= rotationIdentity {
					adapter, output = a, o
					break
				}
			}
			release(&o)
		}
		if output != nil {
			break
		}
		release(&a)
	}
	if output == nil {
		return nil, fmt.Errorf("no unrotated output at %v", want)
	}
	defer release(&adapter)
	defer release(&output)

	d = &dxgi{bounds: want}
	defer func() {
		if err != nil {
			d.Close()
		}
	}()
	if r, _, _ := procD3D11CreateDevice.Call(uintptr(adapter), driverTypeUnknown, 0, 0, 0, 0, sdkVersion,
		uintptr(unsafe.Pointer(&d.device)), 0, uintptr(unsafe.Pointer(&d.context))); r != 0 {
		return nil, fmt.Errorf("D3D11CreateDevice: %#x", uint32(r))
	}
	var output1 unsafe.Pointer
	if hr := comCall(output, vQueryInterface, uintptr(unsafe.Pointer(&iidIDXGIOutput1)), uintptr(unsafe.Pointer(&output1))); hr != 0 {
		return nil, fmt.Errorf("IDXGIOutput1: %#x", hr)
	}
	defer release(&output1)
	if hr := comCall(output1, vOutputDuplicate, uintptr(d.device), uintptr(unsafe.Pointer(&d.dup))); hr != 0 {
		return nil, fmt.Errorf("DuplicateOutput: %#x", hr)
	}

	w, h := want.Dx(), want.Dy()
	var desc [texture2DDescSize]byte
	le := (*[11]uint32)(unsafe.Pointer(&desc[0]))
	*le = [11]uint32{uint32(w), uint32(h), 1, 1, formatB8G8R8A8, 1, 0, usageStaging, 0, cpuAccessRead, 0}
	if hr := comCall(d.device, vDeviceCreateTexture2D, uintptr(unsafe.Pointer(&desc[0])), 0, uintptr(unsafe.Pointer(&d.staging))); hr != 0 {
		return nil, fmt.Errorf("CreateTexture2D: %#x", hr)
	}
	d.imgs = [2]*image.RGBA{image.NewRGBA(want), image.NewRGBA(want)}
	return d, nil
}

// Capture returns the desktop as of now. When nothing changed since the
// last call it returns the same image again.
func (d *dxgi) Capture() (image.Image, error) {
	var info [frameInfoSize]byte
	var res unsafe.Pointer // IDXGIResource
	timeout := uintptr(0)
	if !d.have {
		timeout = 500 // the first frame can take a moment
	}
	switch hr := comCall(d.dup, vDupAcquireNextFrame, timeout, uintptr(unsafe.Pointer(&info[0])), uintptr(unsafe.Pointer(&res))); hr {
	case 0:
	case hrWaitTimeout:
		if d.have {
			return d.imgs[d.turn], nil
		}
		return nil, errNoFrame
	case hrAccessLost:
		return nil, errAccessLost
	default:
		return nil, fmt.Errorf("AcquireNextFrame: %#x", hr)
	}
	defer comCall(d.dup, vDupReleaseFrame)
	defer release(&res)

	lastPresent := *(*int64)(unsafe.Pointer(&info[0]))
	if lastPresent == 0 && d.have {
		// Only the pointer moved; the picture is the same.
		return d.imgs[d.turn], nil
	}
	var tex unsafe.Pointer
	if hr := comCall(res, vQueryInterface, uintptr(unsafe.Pointer(&iidID3D11Texture2)), uintptr(unsafe.Pointer(&tex))); hr != 0 {
		return nil, fmt.Errorf("frame texture: %#x", hr)
	}
	comCall(d.context, vContextCopyResource, uintptr(d.staging), uintptr(tex))
	release(&tex)

	var mapped [mappedResourceSize]byte
	if hr := comCall(d.context, vContextMap, uintptr(d.staging), 0, mapRead, 0, uintptr(unsafe.Pointer(&mapped[0]))); hr != 0 {
		return nil, fmt.Errorf("Map: %#x", hr)
	}
	defer comCall(d.context, vContextUnmap, uintptr(d.staging), 0)
	data := *(*unsafe.Pointer)(unsafe.Pointer(&mapped[0]))
	pitch := int(*(*uint32)(unsafe.Pointer(&mapped[8])))

	w, h := d.bounds.Dx(), d.bounds.Dy()
	if pitch < w*4 || data == nil {
		return nil, errors.New("Map: bad row pitch")
	}
	d.turn ^= 1
	img := d.imgs[d.turn]
	// BGRA -> RGBA, four bytes at a time, on every core.
	parallelRows(h, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			src := unsafe.Slice((*uint32)(unsafe.Add(data, y*pitch)), w)
			dst := unsafe.Slice((*uint32)(unsafe.Pointer(&img.Pix[y*img.Stride])), w)
			for i, v := range src {
				dst[i] = 0xFF000000 | v&0x0000FF00 | (v&0xFF)<<16 | (v>>16)&0xFF
			}
		}
	})
	d.have = true
	return img, nil
}

func (d *dxgi) Close() {
	release(&d.staging)
	release(&d.dup)
	release(&d.context)
	release(&d.device)
}

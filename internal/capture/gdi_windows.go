//go:build windows

package capture

import (
	"errors"
	"image"
	"syscall"
	"time"
	"unsafe"

	"github.com/kbinani/screenshot"
)

var (
	user32                        = syscall.NewLazyDLL("user32.dll")
	gdi32                         = syscall.NewLazyDLL("gdi32.dll")
	procGetDC                     = user32.NewProc("GetDC")
	procReleaseDC                 = user32.NewProc("ReleaseDC")
	procSetProcessDpiAwarenessCtx = user32.NewProc("SetProcessDpiAwarenessContext")
	procCreateCompatibleDC        = gdi32.NewProc("CreateCompatibleDC")
	procCreateDIBSection          = gdi32.NewProc("CreateDIBSection")
	procSelectObject              = gdi32.NewProc("SelectObject")
	procDeleteObject              = gdi32.NewProc("DeleteObject")
	procDeleteDC                  = gdi32.NewProc("DeleteDC")
	procBitBlt                    = gdi32.NewProc("BitBlt")
	procGdiFlush                  = gdi32.NewProc("GdiFlush")
)

const (
	srcCopy           = 0x00CC0020
	perMonitorAwareV2 = ^uintptr(3) // DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2
	// recheckBounds is how often the display size is looked up again, so a
	// resolution change is picked up without querying on every frame.
	recheckBounds = time.Second
)

type bitmapInfoHeader struct {
	size          uint32
	width, height int32
	planes        uint16
	bitCount      uint16
	compression   uint32
	sizeImage     uint32
	xPPM, yPPM    int32
	clrUsed       uint32
	clrImportant  uint32
}

// gdi keeps one screen DC and one DIB section alive between frames, so a
// capture is a single BitBlt plus a channel swap with no allocations.
type gdi struct {
	display  int
	bounds   image.Rectangle
	checked  time.Time
	screenDC uintptr
	memDC    uintptr
	bitmap   uintptr
	old      uintptr
	bits     unsafe.Pointer
	img      *image.RGBA
}

func newNative(display int) nativeSource {
	// Capture in physical pixels on scaled displays.
	if procSetProcessDpiAwarenessCtx.Find() == nil {
		procSetProcessDpiAwarenessCtx.Call(perMonitorAwareV2)
	}
	return &gdi{display: display}
}

func (g *gdi) Capture() (image.Image, error) {
	if time.Since(g.checked) > recheckBounds {
		g.checked = time.Now()
		if screenshot.NumActiveDisplays() <= g.display {
			return nil, errors.New("display not found")
		}
		if b := screenshot.GetDisplayBounds(g.display); b != g.bounds {
			g.release()
			g.bounds = b
		}
	}
	if g.memDC == 0 {
		if err := g.setup(); err != nil {
			g.release()
			return nil, err
		}
	}
	w, h := g.bounds.Dx(), g.bounds.Dy()
	ok, _, _ := procBitBlt.Call(g.memDC, 0, 0, uintptr(w), uintptr(h), g.screenDC,
		uintptr(int32(g.bounds.Min.X)), uintptr(int32(g.bounds.Min.Y)), srcCopy)
	if ok == 0 {
		// Typically the secure desktop (UAC prompt, lock screen). Start over
		// next time in case the DC went stale.
		g.release()
		return nil, errors.New("BitBlt failed")
	}
	procGdiFlush.Call()

	// BGRX -> RGBA, four bytes at a time.
	src := unsafe.Slice((*uint32)(g.bits), w*h)
	dst := unsafe.Slice((*uint32)(unsafe.Pointer(&g.img.Pix[0])), w*h)
	for i, v := range src {
		dst[i] = 0xFF000000 | v&0x0000FF00 | (v&0xFF)<<16 | (v>>16)&0xFF
	}
	return g.img, nil
}

func (g *gdi) setup() error {
	w, h := g.bounds.Dx(), g.bounds.Dy()
	if w <= 0 || h <= 0 {
		return errors.New("empty display")
	}
	g.screenDC, _, _ = procGetDC.Call(0)
	if g.screenDC == 0 {
		return errors.New("GetDC failed")
	}
	g.memDC, _, _ = procCreateCompatibleDC.Call(g.screenDC)
	if g.memDC == 0 {
		return errors.New("CreateCompatibleDC failed")
	}
	hdr := bitmapInfoHeader{
		width:    int32(w),
		height:   -int32(h), // top-down rows
		planes:   1,
		bitCount: 32,
	}
	hdr.size = uint32(unsafe.Sizeof(hdr))
	g.bitmap, _, _ = procCreateDIBSection.Call(g.memDC, uintptr(unsafe.Pointer(&hdr)), 0, uintptr(unsafe.Pointer(&g.bits)), 0, 0)
	if g.bitmap == 0 || g.bits == nil {
		return errors.New("CreateDIBSection failed")
	}
	g.old, _, _ = procSelectObject.Call(g.memDC, g.bitmap)
	g.img = image.NewRGBA(g.bounds)
	return nil
}

func (g *gdi) Close() { g.release() }

func (g *gdi) release() {
	if g.memDC != 0 {
		if g.old != 0 {
			procSelectObject.Call(g.memDC, g.old)
		}
		procDeleteDC.Call(g.memDC)
	}
	if g.bitmap != 0 {
		procDeleteObject.Call(g.bitmap)
	}
	if g.screenDC != 0 {
		procReleaseDC.Call(0, g.screenDC)
	}
	g.memDC, g.bitmap, g.old, g.screenDC, g.bits = 0, 0, 0, 0, nil
}

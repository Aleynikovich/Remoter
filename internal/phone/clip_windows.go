package phone

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"runtime"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32                      = windows.NewLazySystemDLL("user32.dll")
	kernel32                    = windows.NewLazySystemDLL("kernel32.dll")
	procOpenClipboard           = user32.NewProc("OpenClipboard")
	procCloseClipboard          = user32.NewProc("CloseClipboard")
	procEmptyClipboard          = user32.NewProc("EmptyClipboard")
	procSetClipboardData        = user32.NewProc("SetClipboardData")
	procRegisterClipboardFormat = user32.NewProc("RegisterClipboardFormatW")
	procGlobalAlloc             = kernel32.NewProc("GlobalAlloc")
	procGlobalLock              = kernel32.NewProc("GlobalLock")
	procGlobalUnlock            = kernel32.NewProc("GlobalUnlock")
	procGlobalFree              = kernel32.NewProc("GlobalFree")
	procMoveMemory              = kernel32.NewProc("RtlMoveMemory")
)

const (
	cfDIB        = 8
	gmemMoveable = 0x0002
)

// CopyImage puts a PNG on the clipboard twice: as PNG, which keeps
// transparency and is what browsers and chat apps prefer, and as a bitmap
// (CF_DIB) for everything else.
func CopyImage(data []byte) error {
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return err
	}
	dib := toDIB(img)

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	// Another program may hold the clipboard for a moment.
	opened := false
	for range 10 {
		if r, _, _ := procOpenClipboard.Call(0); r != 0 {
			opened = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !opened {
		return errors.New("the clipboard is busy")
	}
	defer procCloseClipboard.Call()
	procEmptyClipboard.Call()
	name, _ := windows.UTF16PtrFromString("PNG")
	if f, _, _ := procRegisterClipboardFormat.Call(uintptr(unsafe.Pointer(name))); f != 0 {
		setClipboard(uint32(f), data)
	}
	return setClipboard(cfDIB, dib)
}

func setClipboard(format uint32, data []byte) error {
	h, _, err := procGlobalAlloc.Call(gmemMoveable, uintptr(len(data)))
	if h == 0 {
		return fmt.Errorf("GlobalAlloc: %w", err)
	}
	p, _, err := procGlobalLock.Call(h)
	if p == 0 {
		procGlobalFree.Call(h)
		return fmt.Errorf("GlobalLock: %w", err)
	}
	procMoveMemory.Call(p, uintptr(unsafe.Pointer(&data[0])), uintptr(len(data)))
	procGlobalUnlock.Call(h)
	// The clipboard owns the memory once this succeeds.
	if r, _, err := procSetClipboardData.Call(uintptr(format), h); r == 0 {
		procGlobalFree.Call(h)
		return fmt.Errorf("SetClipboardData: %w", err)
	}
	return nil
}

// toDIB lays img out as a 32-bit bottom-up device-independent bitmap.
func toDIB(src image.Image) []byte {
	b := src.Bounds()
	rgba := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(rgba, rgba.Rect, src, b.Min, draw.Src)
	w, h := b.Dx(), b.Dy()
	out := make([]byte, 40+w*h*4)
	binary.LittleEndian.PutUint32(out[0:], 40) // biSize
	binary.LittleEndian.PutUint32(out[4:], uint32(w))
	binary.LittleEndian.PutUint32(out[8:], uint32(h)) // positive: bottom-up
	binary.LittleEndian.PutUint16(out[12:], 1)        // planes
	binary.LittleEndian.PutUint16(out[14:], 32)       // bits per pixel
	binary.LittleEndian.PutUint32(out[20:], uint32(w*h*4))
	px := out[40:]
	for y := range h {
		row := rgba.Pix[y*rgba.Stride:]
		dst := px[(h-1-y)*w*4:]
		for x := range w {
			r, g, bl, a := row[x*4], row[x*4+1], row[x*4+2], row[x*4+3]
			dst[x*4], dst[x*4+1], dst[x*4+2], dst[x*4+3] = bl, g, r, a
		}
	}
	return out
}

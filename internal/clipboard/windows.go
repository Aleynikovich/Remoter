//go:build windows

package clipboard

import (
	"errors"
	"runtime"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"
)

var (
	user32                         = syscall.NewLazyDLL("user32.dll")
	kernel32                       = syscall.NewLazyDLL("kernel32.dll")
	procOpenClipboard              = user32.NewProc("OpenClipboard")
	procCloseClipboard             = user32.NewProc("CloseClipboard")
	procEmptyClipboard             = user32.NewProc("EmptyClipboard")
	procGetClipboardData           = user32.NewProc("GetClipboardData")
	procSetClipboardData           = user32.NewProc("SetClipboardData")
	procGetClipboardSequenceNumber = user32.NewProc("GetClipboardSequenceNumber")
	procIsClipboardFormatAvailable = user32.NewProc("IsClipboardFormatAvailable")
	procGlobalAlloc                = kernel32.NewProc("GlobalAlloc")
	procGlobalFree                 = kernel32.NewProc("GlobalFree")
	procGlobalLock                 = kernel32.NewProc("GlobalLock")
	procGlobalUnlock               = kernel32.NewProc("GlobalUnlock")
	procGlobalSize                 = kernel32.NewProc("GlobalSize")
)

const (
	cfUnicodeText = 13
	gmemMoveable  = 0x0002
)

type windows struct{}

func newNative() Clipboard {
	if procGetClipboardSequenceNumber.Find() != nil {
		return nil
	}
	return windows{}
}

func (windows) Seq() uint32 {
	n, _, _ := procGetClipboardSequenceNumber.Call()
	return uint32(n)
}

// open opens the clipboard, retrying briefly because other programs hold it
// for short moments. The caller must stay on the same OS thread until close.
func open() error {
	for range 20 {
		if ok, _, _ := procOpenClipboard.Call(0); ok != 0 {
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return errors.New("clipboard is busy")
}

func (windows) ReadText() (string, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if ok, _, _ := procIsClipboardFormatAvailable.Call(cfUnicodeText); ok == 0 {
		return "", nil
	}
	if err := open(); err != nil {
		return "", err
	}
	defer procCloseClipboard.Call()

	h, _, _ := procGetClipboardData.Call(cfUnicodeText)
	if h == 0 {
		return "", errors.New("GetClipboardData failed")
	}
	size, _, _ := procGlobalSize.Call(h)
	if size/2 > MaxText {
		return "", nil
	}
	p, _, _ := procGlobalLock.Call(h)
	if p == 0 {
		return "", errors.New("GlobalLock failed")
	}
	defer procGlobalUnlock.Call(h)
	units := unsafe.Slice(*(**uint16)(unsafe.Pointer(&p)), size/2)
	for i, u := range units {
		if u == 0 {
			units = units[:i]
			break
		}
	}
	return toLF(string(utf16.Decode(units))), nil
}

func (windows) WriteText(s string) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	units := utf16.Encode([]rune(toCRLF(s) + "\x00"))
	h, _, _ := procGlobalAlloc.Call(gmemMoveable, uintptr(len(units)*2))
	if h == 0 {
		return errors.New("GlobalAlloc failed")
	}
	p, _, _ := procGlobalLock.Call(h)
	if p == 0 {
		procGlobalFree.Call(h)
		return errors.New("GlobalLock failed")
	}
	copy(unsafe.Slice(*(**uint16)(unsafe.Pointer(&p)), len(units)), units)
	procGlobalUnlock.Call(h)

	if err := open(); err != nil {
		procGlobalFree.Call(h)
		return err
	}
	defer procCloseClipboard.Call()
	procEmptyClipboard.Call()
	if r, _, _ := procSetClipboardData.Call(cfUnicodeText, h); r == 0 {
		procGlobalFree.Call(h)
		return errors.New("SetClipboardData failed")
	}
	return nil // the clipboard owns h now
}

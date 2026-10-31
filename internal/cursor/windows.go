//go:build windows

package cursor

import (
	"syscall"
	"unsafe"
)

var (
	user32            = syscall.NewLazyDLL("user32.dll")
	procGetCursorInfo = user32.NewProc("GetCursorInfo")
	procLoadCursorW   = user32.NewProc("LoadCursorW")
)

type cursorInfo struct {
	size   uint32
	flags  uint32
	handle uintptr
	x, y   int32
}

const cursorShowing = 1 // CURSOR_SHOWING

// shapes maps the system cursors (IDC_*) to CSS names. The handles of shared
// system cursors are fixed for the session, so they are looked up once.
var shapes = func() map[uintptr]string {
	ids := map[uintptr]string{
		32512: "default",     // IDC_ARROW
		32513: "text",        // IDC_IBEAM
		32514: "wait",        // IDC_WAIT
		32515: "crosshair",   // IDC_CROSS
		32516: "default",     // IDC_UPARROW
		32642: "nwse-resize", // IDC_SIZENWSE
		32643: "nesw-resize", // IDC_SIZENESW
		32644: "ew-resize",   // IDC_SIZEWE
		32645: "ns-resize",   // IDC_SIZENS
		32646: "move",        // IDC_SIZEALL
		32648: "not-allowed", // IDC_NO
		32649: "pointer",     // IDC_HAND
		32650: "progress",    // IDC_APPSTARTING
		32651: "help",        // IDC_HELP
	}
	m := make(map[uintptr]string)
	if procLoadCursorW.Find() != nil {
		return m
	}
	for id, name := range ids {
		if h, _, _ := procLoadCursorW.Call(0, id); h != 0 {
			m[h] = name
		}
	}
	return m
}()

func shape() string {
	if procGetCursorInfo.Find() != nil {
		return ""
	}
	ci := cursorInfo{}
	ci.size = uint32(unsafe.Sizeof(ci))
	if ok, _, _ := procGetCursorInfo.Call(uintptr(unsafe.Pointer(&ci))); ok == 0 {
		return ""
	}
	if ci.flags&cursorShowing == 0 {
		// Hidden, but Windows also hides it on machines with no mouse
		// attached, so keep showing the viewer's own pointer.
		return "default"
	}
	if name, ok := shapes[ci.handle]; ok {
		return name
	}
	return "default"
}

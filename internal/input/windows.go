//go:build windows && (amd64 || arm64)

package input

import (
	"fmt"
	"image"
	"log"
	"sync"
	"syscall"
	"unicode/utf8"
	"unsafe"

	"github.com/aleynikovich/remoter/internal/protocol"
)

var (
	user32                        = syscall.NewLazyDLL("user32.dll")
	procSendInput                 = user32.NewProc("SendInput")
	procGetSystemMetrics          = user32.NewProc("GetSystemMetrics")
	procSetProcessDpiAwarenessCtx = user32.NewProc("SetProcessDpiAwarenessContext")
	procSetProcessDPIAware        = user32.NewProc("SetProcessDPIAware")
	procSetCursorPos              = user32.NewProc("SetCursorPos")
	procGetCursorPos              = user32.NewProc("GetCursorPos")
	procGetThreadDpiAwarenessCtx  = user32.NewProc("GetThreadDpiAwarenessContext")
	procGetAwarenessFromDpiCtx    = user32.NewProc("GetAwarenessFromDpiAwarenessContext")
)

const (
	inputMouse    = 0
	inputKeyboard = 1

	mouseeventfMove       = 0x0001
	mouseeventfLeftDown   = 0x0002
	mouseeventfLeftUp     = 0x0004
	mouseeventfRightDown  = 0x0008
	mouseeventfRightUp    = 0x0010
	mouseeventfMiddleDown = 0x0020
	mouseeventfMiddleUp   = 0x0040
	mouseeventfWheel      = 0x0800
	mouseeventfVirtualDsk = 0x4000
	mouseeventfAbsolute   = 0x8000

	keyeventfExtended = 0x0001
	keyeventfKeyUp    = 0x0002
	keyeventfUnicode  = 0x0004

	smXVirtualScreen  = 76
	smYVirtualScreen  = 77
	smCXVirtualScreen = 78
	smCYVirtualScreen = 79

	wheelDelta = 120
)

// mouseInput and keyInput mirror the 64-bit layout of the Win32 INPUT struct
// (40 bytes: type, padding, then the MOUSEINPUT / KEYBDINPUT union).
type mouseInput struct {
	typ       uint32
	_         uint32
	dx, dy    int32
	mouseData uint32
	flags     uint32
	time      uint32
	extraInfo uintptr
}

type keyInput struct {
	typ       uint32
	_         uint32
	vk, scan  uint16
	flags     uint32
	time      uint32
	extraInfo uintptr
	_         [8]byte
}

func init() {
	if unsafe.Sizeof(mouseInput{}) != 40 || unsafe.Sizeof(keyInput{}) != 40 {
		panic("input: INPUT struct layout mismatch")
	}
}

type key struct {
	vk       uint16
	scan     uint16
	extended bool
	unicode  bool
}

type windows struct {
	display image.Rectangle
	checked bool // logged where the first move landed

	mu      sync.Mutex
	keys    map[string]map[key]bool // held keys per viewer
	buttons map[string]map[int]bool // held mouse buttons per viewer
}

func newNative(display image.Rectangle) (Handler, error) {
	// Work in physical pixels so mouse positions match the captured image on
	// scaled (125%, 150%...) displays.
	const perMonitorAwareV2 = ^uintptr(3) // DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2 (-4)
	if procSetProcessDpiAwarenessCtx.Find() == nil {
		procSetProcessDpiAwarenessCtx.Call(perMonitorAwareV2)
	} else if procSetProcessDPIAware.Find() == nil {
		procSetProcessDPIAware.Call()
	}
	if err := procSendInput.Find(); err != nil {
		return nil, fmt.Errorf("SendInput unavailable: %w", err)
	}
	return &windows{
		display: display,
		keys:    make(map[string]map[key]bool),
		buttons: make(map[string]map[int]bool),
	}, nil
}

func (w *windows) HandleInput(viewer string, in protocol.Input) {
	w.mu.Lock()
	defer w.mu.Unlock()
	switch in.Kind {
	case "mousemove":
		w.move(in.X, in.Y)
	case "mousedown", "mouseup":
		down := in.Kind == "mousedown"
		flag := buttonFlag(in.Button, down)
		if flag == 0 {
			return
		}
		w.move(in.X, in.Y)
		sendMouse(0, 0, 0, flag)
		setHeld(w.buttons, viewer, in.Button, down)
	case "wheel":
		w.move(in.X, in.Y)
		// Browsers report pixels (about 100 per notch) with down positive;
		// Windows wants 120 per notch with up positive.
		amount := int32(-in.DeltaY * wheelDelta / 100)
		if amount == 0 {
			return
		}
		sendMouse(0, 0, uint32(amount), mouseeventfWheel)
	case "keydown", "keyup":
		k, ok := lookupKey(in)
		if !ok {
			log.Printf("input: no mapping for key code=%q key=%q", in.Code, in.Key)
			return
		}
		down := in.Kind == "keydown"
		sendKey(k, down)
		setHeld(w.keys, viewer, k, down)
	}
}

func (w *windows) ReleaseAll(viewer string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for k := range w.keys[viewer] {
		sendKey(k, false)
	}
	for b := range w.buttons[viewer] {
		sendMouse(0, 0, 0, buttonFlag(b, false))
	}
	delete(w.keys, viewer)
	delete(w.buttons, viewer)
}

func (w *windows) SetDisplay(display image.Rectangle) {
	w.mu.Lock()
	w.display = display
	w.checked = false
	w.mu.Unlock()
}

func (w *windows) move(x, y float64) {
	p := toScreen(w.display, x, y)
	moveTo(p)
	if !w.checked {
		w.checked = true
		got, _ := cursorPos()
		log.Printf("input: display %v, virtual screen %d,%d %dx%d, DPI awareness %d, pointer asked at %v and now at %v", w.display,
			metric(smXVirtualScreen), metric(smYVirtualScreen), metric(smCXVirtualScreen), metric(smCYVirtualScreen),
			dpiAwareness(), p, got)
	}
}

// dpiAwareness is this thread's DPI awareness: 0 unaware, 1 system, 2 per
// monitor, -1 unknown.
func dpiAwareness() int {
	if procGetThreadDpiAwarenessCtx.Find() != nil || procGetAwarenessFromDpiCtx.Find() != nil {
		return -1
	}
	ctx, _, _ := procGetThreadDpiAwarenessCtx.Call()
	a, _, _ := procGetAwarenessFromDpiCtx.Call(ctx)
	return int(int32(a))
}

// cursorPos is where the pointer is, in physical pixels.
func cursorPos() (image.Point, bool) {
	var pt struct{ x, y int32 }
	if ok, _, _ := procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt))); ok == 0 {
		return image.Point{}, false
	}
	return image.Pt(int(pt.x), int(pt.y)), true
}

func lookupKey(in protocol.Input) (key, bool) {
	if vk, ext, ok := virtualKey(in.Code); ok {
		return key{vk: vk, extended: ext}, true
	}
	// Keys without a physical mapping (some layouts, IME) are typed as text.
	if r, size := utf8.DecodeRuneInString(in.Key); r != utf8.RuneError && size == len(in.Key) && r <= 0xFFFF {
		return key{scan: uint16(r), unicode: true}, true
	}
	return key{}, false
}

func buttonFlag(button int, down bool) uint32 {
	// Browser button numbers: 0 left, 1 middle, 2 right.
	switch {
	case button == 0 && down:
		return mouseeventfLeftDown
	case button == 0:
		return mouseeventfLeftUp
	case button == 1 && down:
		return mouseeventfMiddleDown
	case button == 1:
		return mouseeventfMiddleUp
	case button == 2 && down:
		return mouseeventfRightDown
	case button == 2:
		return mouseeventfRightUp
	}
	return 0
}

func setHeld[T comparable](held map[string]map[T]bool, viewer string, v T, down bool) {
	if down {
		if held[viewer] == nil {
			held[viewer] = make(map[T]bool)
		}
		held[viewer][v] = true
	} else {
		delete(held[viewer], v)
	}
}

func metric(index int) int {
	r, _, _ := procGetSystemMetrics.Call(uintptr(index))
	return int(int32(r))
}

func sendMouse(dx, dy int32, data, flags uint32) {
	in := mouseInput{typ: inputMouse, dx: dx, dy: dy, mouseData: data, flags: flags}
	send(unsafe.Pointer(&in))
}

func sendKey(k key, down bool) {
	var flags uint32
	if !down {
		flags |= keyeventfKeyUp
	}
	if k.extended {
		flags |= keyeventfExtended
	}
	if k.unicode {
		flags |= keyeventfUnicode
	}
	in := keyInput{typ: inputKeyboard, vk: k.vk, scan: k.scan, flags: flags}
	send(unsafe.Pointer(&in))
}

func send(in unsafe.Pointer) {
	n, _, err := procSendInput.Call(1, uintptr(in), 40)
	if n == 0 {
		// Usually UIPI: the focused window belongs to a process running as
		// administrator, or the secure desktop (UAC, lock screen) is active.
		log.Printf("input: SendInput blocked: %v", err)
	}
}

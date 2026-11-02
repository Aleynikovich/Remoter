//go:build windows

package kvm

import (
	"context"
	"fmt"
	"image"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"syscall"
	"unsafe"
)

var (
	user32   = syscall.NewLazyDLL("user32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")

	procSetWindowsHookEx        = user32.NewProc("SetWindowsHookExW")
	procUnhookWindowsHookEx     = user32.NewProc("UnhookWindowsHookEx")
	procCallNextHookEx          = user32.NewProc("CallNextHookEx")
	procGetMessage              = user32.NewProc("GetMessageW")
	procTranslateMessage        = user32.NewProc("TranslateMessage")
	procDispatchMessage         = user32.NewProc("DispatchMessageW")
	procPostThreadMessage       = user32.NewProc("PostThreadMessageW")
	procSetCursorPos            = user32.NewProc("SetCursorPos")
	procEnumDisplayMonitors     = user32.NewProc("EnumDisplayMonitors")
	procGetMonitorInfo          = user32.NewProc("GetMonitorInfoW")
	procSetProcessDpiAwareness  = user32.NewProc("SetProcessDpiAwarenessContext")
	procMapVirtualKey           = user32.NewProc("MapVirtualKeyW")
	procRegisterClassEx         = user32.NewProc("RegisterClassExW")
	procCreateWindowEx          = user32.NewProc("CreateWindowExW")
	procDestroyWindow           = user32.NewProc("DestroyWindow")
	procDefWindowProc           = user32.NewProc("DefWindowProcW")
	procRegisterRawInputDevices = user32.NewProc("RegisterRawInputDevices")
	procGetRawInputData         = user32.NewProc("GetRawInputData")
	procOpenInputDesktop        = user32.NewProc("OpenInputDesktop")
	procCloseDesktop            = user32.NewProc("CloseDesktop")
	procGetUserObjectInfo       = user32.NewProc("GetUserObjectInformationW")
	procGetModuleHandle         = kernel32.NewProc("GetModuleHandleW")
	procGetCurrentThreadID      = kernel32.NewProc("GetCurrentThreadId")
)

const (
	whKeyboardLL = 13
	whMouseLL    = 14

	wmQuit        = 0x0012
	wmInput       = 0x00FF
	wmKeyDown     = 0x0100
	wmSysKeyDown  = 0x0104
	wmMouseMove   = 0x0200
	wmLButtonDown = 0x0201
	wmLButtonUp   = 0x0202
	wmRButtonDown = 0x0204
	wmRButtonUp   = 0x0205
	wmMButtonDown = 0x0207
	wmMButtonUp   = 0x0208
	wmMouseWheel  = 0x020A
	wmXButtonDown = 0x020B
	wmXButtonUp   = 0x020C
	wmMouseHWheel = 0x020E

	llmhfInjected        = 0x01
	llmhfLowerILInjected = 0x02
	llkhfExtended        = 0x01
	llkhfLowerILInjected = 0x02
	llkhfInjected        = 0x10

	mapvkVKToVSCEx = 4

	ridevInputSink = 0x00000100
	ridInput       = 0x10000003
	rimTypeMouse   = 0
	mouseMoveAbs   = 0x01

	perMonitorAwareV2 = ^uintptr(3) // DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2 (-4)
	hwndMessage       = ^uintptr(2) // HWND_MESSAGE (-3)
)

type point struct{ X, Y int32 }

type msllHook struct {
	Pt        point
	MouseData uint32
	Flags     uint32
	Time      uint32
	Extra     uintptr
}

type kbdllHook struct {
	VK, Scan, Flags, Time uint32
	Extra                 uintptr
}

type msg struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      point
	_       uint32
}

type rect struct{ Left, Top, Right, Bottom int32 }

type monitorInfo struct {
	Size    uint32
	Monitor rect
	Work    rect
	Flags   uint32
}

type wndClassEx struct {
	Size       uint32
	Style      uint32
	WndProc    uintptr
	ClsExtra   int32
	WndExtra   int32
	Instance   uintptr
	Icon       uintptr
	Cursor     uintptr
	Background uintptr
	MenuName   *uint16
	ClassName  *uint16
	IconSm     uintptr
}

type rawInputDevice struct {
	UsagePage uint16
	Usage     uint16
	Flags     uint32
	Target    uintptr
}

// rawInputHeader is RAWINPUTHEADER; a RAWMOUSE follows it.
type rawInputHeader struct {
	Type   uint32
	Size   uint32
	Device uintptr
	WParam uintptr
}

type rawMouse struct {
	Flags      uint16
	_          uint16
	Buttons    uint32
	RawButtons uint32
	LastX      int32
	LastY      int32
	Extra      uint32
}

// winCapture captures with low-level hooks, which see (and can swallow)
// all keyboard and mouse input on this desktop before any program does.
type winCapture struct {
	tid atomic.Uint32
}

func newCapture() (capture, error) {
	if procSetWindowsHookEx.Find() != nil {
		return nil, ErrUnsupported
	}
	// Hook positions, monitors and SetCursorPos then all use physical
	// pixels in one space, whatever each monitor's scaling.
	if procSetProcessDpiAwareness.Find() == nil {
		procSetProcessDpiAwareness.Call(perMonitorAwareV2)
	}
	return &winCapture{}, nil
}

func (w *winCapture) Warp(p image.Point) {
	procSetCursorPos.Call(uintptr(int32(p.X)), uintptr(int32(p.Y)))
}

func (w *winCapture) Screens() []image.Rectangle {
	monitorsMu.Lock()
	defer monitorsMu.Unlock()
	monitors = nil
	procEnumDisplayMonitors.Call(0, 0, callbacks.monitor, 0)
	rs := monitors
	// Left to right, as Remoter numbers displays elsewhere.
	sort.SliceStable(rs, func(i, j int) bool {
		if rs[i].Min.X != rs[j].Min.X {
			return rs[i].Min.X < rs[j].Min.X
		}
		return rs[i].Min.Y < rs[j].Min.Y
	})
	return rs
}

func (w *winCapture) OnUserDesktop() bool {
	const desktopReadObjects = 0x0001
	h, _, _ := procOpenInputDesktop.Call(0, 0, desktopReadObjects)
	if h == 0 {
		// The secure desktop (lock screen, UAC) refuses a user's program.
		return false
	}
	defer procCloseDesktop.Call(h)
	var buf [64]uint16
	var n uint32
	if r, _, _ := procGetUserObjectInfo.Call(h, 2 /* UOI_NAME */, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)*2), uintptr(unsafe.Pointer(&n))); r == 0 {
		return true
	}
	return syscall.UTF16ToString(buf[:]) == "Default"
}

// The hook callbacks are process-wide; one capture runs at a time.
// Windows callbacks are made once: Go can only make a limited number.
var (
	hookMachine atomic.Pointer[Machine]
	mouseHook   uintptr
	keyHook     uintptr
	callbacks   struct{ mouse, key, wnd, monitor uintptr }

	monitorsMu sync.Mutex
	monitors   []image.Rectangle
)

func init() {
	callbacks.mouse = syscall.NewCallback(mouseProc)
	callbacks.key = syscall.NewCallback(keyProc)
	callbacks.wnd = syscall.NewCallback(wndProc)
	callbacks.monitor = syscall.NewCallback(func(hmon, hdc, lprc, data uintptr) uintptr {
		mi := monitorInfo{Size: uint32(unsafe.Sizeof(monitorInfo{}))}
		if ok, _, _ := procGetMonitorInfo.Call(hmon, uintptr(unsafe.Pointer(&mi))); ok != 0 {
			r := mi.Monitor
			monitors = append(monitors, image.Rect(int(r.Left), int(r.Top), int(r.Right), int(r.Bottom)))
		}
		return 1
	})
}

// Run installs the hooks on a thread of its own, which must keep pumping
// messages: Windows silently removes a low-level hook that takes longer
// than a second to answer.
func (w *winCapture) Run(ctx context.Context, m *Machine) error {
	errc := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		tid, _, _ := procGetCurrentThreadID.Call()
		w.tid.Store(uint32(tid))
		hookMachine.Store(m)
		defer hookMachine.Store(nil)
		mod, _, _ := procGetModuleHandle.Call(0)
		var err error
		mouseHook, _, err = procSetWindowsHookEx.Call(whMouseLL, callbacks.mouse, mod, 0)
		if mouseHook == 0 {
			errc <- fmt.Errorf("install the mouse hook: %v", err)
			return
		}
		defer procUnhookWindowsHookEx.Call(mouseHook)
		keyHook, _, err = procSetWindowsHookEx.Call(whKeyboardLL, callbacks.key, mod, 0)
		if keyHook == 0 {
			errc <- fmt.Errorf("install the keyboard hook: %v", err)
			return
		}
		defer procUnhookWindowsHookEx.Call(keyHook)
		// Raw mouse input measures pushing against the edge, which the
		// pointer (stopped by the edge) cannot show. Without it, edge
		// reports are counted instead.
		if hwnd := rawWindow(mod); hwnd != 0 {
			defer procDestroyWindow.Call(hwnd)
		}
		errc <- nil
		var mg msg
		for {
			r, _, _ := procGetMessage.Call(uintptr(unsafe.Pointer(&mg)), 0, 0, 0)
			if int32(r) <= 0 {
				return
			}
			procTranslateMessage.Call(uintptr(unsafe.Pointer(&mg)))
			procDispatchMessage.Call(uintptr(unsafe.Pointer(&mg)))
		}
	}()
	if err := <-errc; err != nil {
		return err
	}
	<-ctx.Done()
	m.Return("stopping")
	procPostThreadMessage.Call(uintptr(w.tid.Load()), wmQuit, 0, 0)
	return nil
}

// rawWindow makes a hidden window that receives raw mouse input, or 0.
func rawWindow(mod uintptr) uintptr {
	name, _ := syscall.UTF16PtrFromString("RemoterKVMRawInput")
	wc := wndClassEx{WndProc: callbacks.wnd, Instance: mod, ClassName: name}
	wc.Size = uint32(unsafe.Sizeof(wc))
	procRegisterClassEx.Call(uintptr(unsafe.Pointer(&wc)))
	hwnd, _, _ := procCreateWindowEx.Call(0, uintptr(unsafe.Pointer(name)), 0, 0, 0, 0, 0, 0, hwndMessage, 0, mod, 0)
	if hwnd == 0 {
		return 0
	}
	dev := rawInputDevice{UsagePage: 1, Usage: 2, Flags: ridevInputSink, Target: hwnd}
	if ok, _, _ := procRegisterRawInputDevices.Call(uintptr(unsafe.Pointer(&dev)), 1, unsafe.Sizeof(dev)); ok == 0 {
		procDestroyWindow.Call(hwnd)
		return 0
	}
	return hwnd
}

func wndProc(hwnd, umsg, wparam, lparam uintptr) uintptr {
	if umsg == wmInput {
		var buf [64]byte
		size := uint32(len(buf))
		n, _, _ := procGetRawInputData.Call(lparam, ridInput, uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)), unsafe.Sizeof(rawInputHeader{}))
		if int32(n) > 0 && uintptr(n) >= unsafe.Sizeof(rawInputHeader{})+unsafe.Sizeof(rawMouse{}) {
			h := (*rawInputHeader)(unsafe.Pointer(&buf[0]))
			if h.Type == rimTypeMouse {
				rm := (*rawMouse)(unsafe.Pointer(&buf[unsafe.Sizeof(rawInputHeader{})]))
				if rm.Flags&mouseMoveAbs == 0 && (rm.LastX != 0 || rm.LastY != 0) {
					if m := hookMachine.Load(); m != nil {
						m.Raw(int(rm.LastX), int(rm.LastY))
					}
				}
			}
		}
	}
	r, _, _ := procDefWindowProc.Call(hwnd, umsg, wparam, lparam)
	return r
}

// The hooks' lparam is declared as the struct it points to, which
// NewCallback allows and saves converting a uintptr to a pointer.
func mouseProc(code, wparam uintptr, h *msllHook) uintptr {
	lparam := uintptr(unsafe.Pointer(h))
	m := hookMachine.Load()
	if int32(code) < 0 || m == nil {
		return next(mouseHook, code, wparam, lparam)
	}
	e := MouseEvent{
		Pt:       image.Pt(int(h.Pt.X), int(h.Pt.Y)),
		Injected: h.Flags&(llmhfInjected|llmhfLowerILInjected) != 0,
	}
	hi := int(int16(h.MouseData >> 16))
	switch wparam {
	case wmMouseMove:
		e.Kind = MouseMove
	case wmLButtonDown, wmLButtonUp:
		e.Kind, e.Button = upDown(wparam == wmLButtonDown), 0
	case wmMButtonDown, wmMButtonUp:
		e.Kind, e.Button = upDown(wparam == wmMButtonDown), 1
	case wmRButtonDown, wmRButtonUp:
		e.Kind, e.Button = upDown(wparam == wmRButtonDown), 2
	case wmXButtonDown, wmXButtonUp:
		e.Kind, e.Button = upDown(wparam == wmXButtonDown), 2+hi // XBUTTON1 back, 2 forward
	case wmMouseWheel:
		e.Kind, e.Data = MouseWheel, hi
	case wmMouseHWheel:
		e.Kind, e.Data = MouseHWheel, hi
	default:
		return next(mouseHook, code, wparam, lparam)
	}
	if m.Mouse(e) {
		return 1
	}
	return next(mouseHook, code, wparam, lparam)
}

func upDown(down bool) int {
	if down {
		return MouseDown
	}
	return MouseUp
}

func keyProc(code, wparam uintptr, h *kbdllHook) uintptr {
	lparam := uintptr(unsafe.Pointer(h))
	m := hookMachine.Load()
	if int32(code) < 0 || m == nil {
		return next(keyHook, code, wparam, lparam)
	}
	e := KeyEvent{
		Down:     wparam == wmKeyDown || wparam == wmSysKeyDown,
		Scan:     h.Scan,
		Extended: h.Flags&llkhfExtended != 0,
		VK:       h.VK,
		Injected: h.Flags&(llkhfInjected|llkhfLowerILInjected) != 0,
	}
	if e.Scan == 0 {
		// Some keyboards and IMEs leave the scan code out.
		sc, _, _ := procMapVirtualKey.Call(uintptr(h.VK), mapvkVKToVSCEx)
		e.Scan, e.Extended = uint32(sc&0xFF), sc&0xFF00 == 0xE000
	}
	if m.Key(e) {
		return 1
	}
	return next(keyHook, code, wparam, lparam)
}

func next(hook, code, wparam, lparam uintptr) uintptr {
	r, _, _ := procCallNextHookEx.Call(hook, code, wparam, lparam)
	return r
}

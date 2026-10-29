//go:build windows && (amd64 || arm64)

package input

import (
	"image"
	"runtime"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

var (
	procRegisterClassEx  = user32.NewProc("RegisterClassExW")
	procCreateWindowEx   = user32.NewProc("CreateWindowExW")
	procDestroyWindow    = user32.NewProc("DestroyWindow")
	procDefWindowProc    = user32.NewProc("DefWindowProcW")
	procPeekMessage      = user32.NewProc("PeekMessageW")
	procTranslateMessage = user32.NewProc("TranslateMessage")
	procDispatchMessage  = user32.NewProc("DispatchMessageW")
	procTrackMouseEvent  = user32.NewProc("TrackMouseEvent")
	procSetForeground    = user32.NewProc("SetForegroundWindow")
	procSystemParamsInfo = user32.NewProc("SystemParametersInfoW")
)

type wndClassEx struct {
	size, style                uint32
	wndProc                    uintptr
	clsExtra, wndExtra         int32
	instance, icon, cursor, bg uintptr
	menuName, className        *uint16
	iconSm                     uintptr
}

type msg struct {
	hwnd           uintptr
	message        uint32
	wParam, lParam uintptr
	time           uint32
	pt             [2]int32
	private        uint32
}

type trackMouse struct {
	size, flags uint32
	hwnd        uintptr
	hoverTime   uint32
}

var hover struct {
	hwnd     uintptr
	tracking bool
	moves    int
	hovered  bool
}

func wndProc(hwnd uintptr, m uint32, w, l uintptr) uintptr {
	switch m {
	case 0x0200: // WM_MOUSEMOVE
		hover.moves++
		if !hover.tracking {
			tm := trackMouse{flags: 1 | 2, hwnd: hwnd, hoverTime: 100} // TME_HOVER | TME_LEAVE
			tm.size = uint32(unsafe.Sizeof(tm))
			procTrackMouseEvent.Call(uintptr(unsafe.Pointer(&tm)))
			hover.tracking = true
		}
	case 0x02A1: // WM_MOUSEHOVER
		hover.hovered = true
	case 0x02A3: // WM_MOUSELEAVE
		hover.tracking = false
	}
	r, _, _ := procDefWindowProc.Call(hwnd, uintptr(m), w, l)
	return r
}

func pump(d time.Duration) {
	end := time.Now().Add(d)
	var m msg
	for time.Now().Before(end) {
		for {
			r, _, _ := procPeekMessage.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0, 1) // PM_REMOVE
			if r == 0 {
				break
			}
			procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
			procDispatchMessage.Call(uintptr(unsafe.Pointer(&m)))
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// TestHoverReachesWindows checks that pointer moves make Windows send a
// window WM_MOUSEHOVER, which tooltips, taskbar previews and game hover
// depend on, with mouse acceleration on and off.
func TestHoverReachesWindows(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	vx, vy := metric(smXVirtualScreen), metric(smYVirtualScreen)
	if vw := metric(smCXVirtualScreen); vw < 600 {
		t.Skip("no desktop")
	}
	if ok, _, _ := procSetCursorPos.Call(uintptr(vx+1), uintptr(vy+1)); ok == 0 {
		t.Skip("no interactive desktop")
	}
	name, _ := syscall.UTF16PtrFromString("RemoterHoverTest")
	wc := wndClassEx{wndProc: syscall.NewCallback(wndProc), className: name}
	wc.size = uint32(unsafe.Sizeof(wc))
	procRegisterClassEx.Call(uintptr(unsafe.Pointer(&wc)))
	const wsPopup, wsVisible, wsExTopmost = 0x80000000, 0x10000000, 0x8
	x0, y0 := vx+200, vy+200
	hwnd, _, err := procCreateWindowEx.Call(wsExTopmost, uintptr(unsafe.Pointer(name)), 0, wsPopup|wsVisible,
		uintptr(x0), uintptr(y0), 300, 300, 0, 0, 0, 0)
	if hwnd == 0 {
		t.Fatalf("CreateWindowEx: %v", err)
	}
	defer procDestroyWindow.Call(hwnd)
	procSetForeground.Call(hwnd)
	pump(300 * time.Millisecond)

	var saved [3]int32
	procSystemParamsInfo.Call(3, 0, uintptr(unsafe.Pointer(&saved)), 0) // SPI_GETMOUSE
	defer procSystemParamsInfo.Call(4, 0, uintptr(unsafe.Pointer(&saved)), 0)

	methods := map[string]func(p image.Point){
		"moveTo":    moveTo,
		"setCursor": setCursor,
		"relative": func(p image.Point) {
			from, _ := cursorPos()
			sendMouse(int32(p.X-from.X), int32(p.Y-from.Y), 0, mouseeventfMove)
		},
	}
	for _, accel := range []bool{false, true} {
		params := [3]int32{0, 0, 0}
		if accel {
			params = [3]int32{6, 10, 1} // Windows' default "enhance pointer precision"
		}
		procSystemParamsInfo.Call(4, 0, uintptr(unsafe.Pointer(&params)), 0) // SPI_SETMOUSE, not saved
		for _, name := range []string{"moveTo", "setCursor", "relative"} {
			move := methods[name]
			setCursor(image.Pt(vx+5, vy+5))
			pump(300 * time.Millisecond)
			hover.moves, hover.hovered = 0, false
			for i := 0; i < 6; i++ {
				move(image.Pt(x0+60+i*20, y0+60+i*15))
				pump(16 * time.Millisecond)
			}
			pump(600 * time.Millisecond)
			got, _ := cursorPos()
			t.Logf("accel %v, %s: %d WM_MOUSEMOVE, hover %v, pointer at %v (wanted %v)",
				accel, name, hover.moves, hover.hovered, got, image.Pt(x0+160, y0+135))
			if name == "moveTo" && !hover.hovered {
				t.Errorf("accel %v: moveTo did not make the window hover", accel)
			}
		}
	}
}

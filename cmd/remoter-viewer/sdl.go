//go:build linux || windows

package main

import (
	"fmt"
	"unsafe"

	"github.com/ebitengine/purego"
)

// SDL 2 is loaded at run time, so the viewer builds without cgo: on Linux
// from the system (every desktop has it, on current distributions as
// sdl2-compat on top of SDL 3), on Windows from SDL's official release. Constants and event layouts follow SDL2's headers, which have
// kept them stable since 2.0.

const (
	sdlInitVideo = 0x20

	sdlWindowFullscreenDesktop = 0x1001
	sdlWindowResizable         = 0x20
	sdlWindowAllowHighDPI      = 0x2000
	sdlWindowPosCentered       = 0x2FFF0000

	sdlRendererAccelerated = 0x2

	sdlPixelFormatIYUV     = 0x56555949
	sdlPixelFormatABGR8888 = 0x16762004 // bytes R, G, B, A: Go's image.RGBA
	sdlTextureStatic       = 0
	sdlTextureStreaming    = 1
	sdlBlendBlend          = 1
	sdlScaleNearest        = 0
	sdlScaleLinear         = 1
	sdlYUVBT601            = 1

	sdlQuit            = 0x100
	sdlWindowEvent     = 0x200
	sdlKeyDown         = 0x300
	sdlKeyUp           = 0x301
	sdlTextInput       = 0x303
	sdlMouseMotion     = 0x400
	sdlMouseButtonDown = 0x401
	sdlMouseButtonUp   = 0x402
	sdlMouseWheel      = 0x403
	sdlClipboardUpdate = 0x900
	sdlRenderReset     = 0x2000 // render targets reset; 0x2001 device reset

	sdlWindowEventFocusGained = 12
	sdlWindowEventFocusLost   = 13

	kmodShift = 0x0003
	kmodCtrl  = 0x00C0
	kmodAlt   = 0x0300
	kmodGUI   = 0x0C00

	sdlMouseWheelFlipped = 1
)

// sdlRect is SDL_Rect.
type sdlRect struct{ X, Y, W, H int32 }

// sdlEvent is the SDL_Event union.
type sdlEvent [56]byte

func (e *sdlEvent) u8(off int) uint8   { return e[off] }
func (e *sdlEvent) u16(off int) uint16 { return *(*uint16)(unsafe.Pointer(&e[off])) }
func (e *sdlEvent) u32(off int) uint32 { return *(*uint32)(unsafe.Pointer(&e[off])) }
func (e *sdlEvent) i32(off int) int32  { return *(*int32)(unsafe.Pointer(&e[off])) }
func (e *sdlEvent) f32(off int) float32 {
	return *(*float32)(unsafe.Pointer(&e[off]))
}
func (e *sdlEvent) typ() uint32 { return e.u32(0) }

var sdl struct {
	Init                   func(flags uint32) int32
	Quit                   func()
	GetError               func() string
	GetCurrentVideoDriver  func() string
	SetHint                func(name, value string) int32
	CreateWindow           func(title string, x, y, w, h int32, flags uint32) uintptr
	SetWindowTitle         func(win uintptr, title string)
	SetWindowFullscreen    func(win uintptr, flags uint32) int32
	GetWindowFlags         func(win uintptr) uint32
	GetWindowSize          func(win uintptr, w, h *int32)
	SetWindowMinimumSize   func(win uintptr, w, h int32)
	SetWindowKeyboardGrab  func(win uintptr, grabbed int32)
	SetWindowMouseGrab     func(win uintptr, grabbed int32)
	RaiseWindow            func(win uintptr)
	CreateRenderer         func(win uintptr, index int32, flags uint32) uintptr
	GetRendererOutputSize  func(r uintptr, w, h *int32) int32
	SetRenderDrawColor     func(r uintptr, red, green, blue, alpha uint8) int32
	RenderClear            func(r uintptr) int32
	RenderFillRect         func(r uintptr, rect *sdlRect) int32
	RenderCopy             func(r uintptr, tex uintptr, src, dst *sdlRect) int32
	RenderPresent          func(r uintptr)
	SetRenderDrawBlendMode func(r uintptr, mode int32) int32
	CreateTexture          func(r uintptr, format uint32, access, w, h int32) uintptr
	DestroyTexture         func(tex uintptr)
	UpdateTexture          func(tex uintptr, rect *sdlRect, pixels unsafe.Pointer, pitch int32) int32
	UpdateYUVTexture       func(tex uintptr, rect *sdlRect, y unsafe.Pointer, yPitch int32, u unsafe.Pointer, uPitch int32, v unsafe.Pointer, vPitch int32) int32
	SetTextureBlendMode    func(tex uintptr, mode int32) int32
	SetTextureScaleMode    func(tex uintptr, mode int32) int32
	SetYUVConversionMode   func(mode int32)
	WaitEventTimeout       func(ev *sdlEvent, ms int32) int32
	PollEvent              func(ev *sdlEvent) int32
	PushEvent              func(ev *sdlEvent) int32
	RegisterEvents         func(n int32) uint32
	StartTextInput         func()
	StopTextInput          func()
	GetClipboardText       func() uintptr
	SetClipboardText       func(text string) int32
	Free                   func(p uintptr)
	CreateSystemCursor     func(id int32) uintptr
	SetCursor              func(c uintptr)
	ShowCursor             func(toggle int32) int32
	GetKeyFromScancode     func(sc int32) int32
}

// loadSDL opens SDL 2 and binds the functions above.
func loadSDL() error {
	sym, err := openSDL()
	if err != nil {
		return err
	}
	bind := func(fn any, name string) {
		if err != nil {
			return
		}
		addr, e := sym(name)
		if e != nil || addr == 0 {
			err = fmt.Errorf("SDL2 is too old: %s missing", name)
			return
		}
		purego.RegisterFunc(fn, addr)
	}
	bind(&sdl.Init, "SDL_Init")
	bind(&sdl.Quit, "SDL_Quit")
	bind(&sdl.GetError, "SDL_GetError")
	bind(&sdl.GetCurrentVideoDriver, "SDL_GetCurrentVideoDriver")
	bind(&sdl.SetHint, "SDL_SetHint")
	bind(&sdl.CreateWindow, "SDL_CreateWindow")
	bind(&sdl.SetWindowTitle, "SDL_SetWindowTitle")
	bind(&sdl.SetWindowFullscreen, "SDL_SetWindowFullscreen")
	bind(&sdl.GetWindowFlags, "SDL_GetWindowFlags")
	bind(&sdl.GetWindowSize, "SDL_GetWindowSize")
	bind(&sdl.SetWindowMinimumSize, "SDL_SetWindowMinimumSize")
	bind(&sdl.SetWindowKeyboardGrab, "SDL_SetWindowKeyboardGrab")
	bind(&sdl.SetWindowMouseGrab, "SDL_SetWindowMouseGrab")
	bind(&sdl.RaiseWindow, "SDL_RaiseWindow")
	bind(&sdl.CreateRenderer, "SDL_CreateRenderer")
	bind(&sdl.GetRendererOutputSize, "SDL_GetRendererOutputSize")
	bind(&sdl.SetRenderDrawColor, "SDL_SetRenderDrawColor")
	bind(&sdl.RenderClear, "SDL_RenderClear")
	bind(&sdl.RenderFillRect, "SDL_RenderFillRect")
	bind(&sdl.RenderCopy, "SDL_RenderCopy")
	bind(&sdl.RenderPresent, "SDL_RenderPresent")
	bind(&sdl.SetRenderDrawBlendMode, "SDL_SetRenderDrawBlendMode")
	bind(&sdl.CreateTexture, "SDL_CreateTexture")
	bind(&sdl.DestroyTexture, "SDL_DestroyTexture")
	bind(&sdl.UpdateTexture, "SDL_UpdateTexture")
	bind(&sdl.UpdateYUVTexture, "SDL_UpdateYUVTexture")
	bind(&sdl.SetTextureBlendMode, "SDL_SetTextureBlendMode")
	bind(&sdl.SetTextureScaleMode, "SDL_SetTextureScaleMode")
	bind(&sdl.SetYUVConversionMode, "SDL_SetYUVConversionMode")
	bind(&sdl.WaitEventTimeout, "SDL_WaitEventTimeout")
	bind(&sdl.PollEvent, "SDL_PollEvent")
	bind(&sdl.PushEvent, "SDL_PushEvent")
	bind(&sdl.RegisterEvents, "SDL_RegisterEvents")
	bind(&sdl.StartTextInput, "SDL_StartTextInput")
	bind(&sdl.StopTextInput, "SDL_StopTextInput")
	bind(&sdl.GetClipboardText, "SDL_GetClipboardText")
	bind(&sdl.SetClipboardText, "SDL_SetClipboardText")
	bind(&sdl.Free, "SDL_free")
	bind(&sdl.CreateSystemCursor, "SDL_CreateSystemCursor")
	bind(&sdl.SetCursor, "SDL_SetCursor")
	bind(&sdl.ShowCursor, "SDL_ShowCursor")
	bind(&sdl.GetKeyFromScancode, "SDL_GetKeyFromScancode")
	return err
}

// clipboardText returns the local clipboard as text, or "" when it holds
// none.
func clipboardText() string {
	p := sdl.GetClipboardText()
	if p == 0 {
		return ""
	}
	defer sdl.Free(p)
	return goString(p)
}

// goString copies a NUL-terminated C string.
func goString(p uintptr) string {
	start := cPointer(p)
	n := 0
	for *(*byte)(unsafe.Add(start, n)) != 0 {
		n++
	}
	return string(unsafe.Slice((*byte)(start), n))
}

// cPointer turns an address returned by a C library into a pointer. The
// memory is not Go's, so the garbage collector never moves it.
func cPointer(p uintptr) unsafe.Pointer {
	return *(*unsafe.Pointer)(unsafe.Pointer(&p))
}

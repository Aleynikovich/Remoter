//go:build linux || windows

package main

import (
	"errors"
	"fmt"
	"image"

	"github.com/aleynikovich/remoter/internal/h264"
)

const sdlWindowHidden = 0x8

// selfTest checks, without showing anything, that SDL opens a window and
// renderer here and that a frame from the host's encoder decodes and uploads.
// CI runs it on Windows, where the libraries are fetched at run time.
func selfTest() error {
	if err := loadSDL(); err != nil {
		return err
	}
	if sdl.Init(sdlInitVideo) != 0 {
		return fmt.Errorf("start SDL: %s", sdl.GetError())
	}
	defer sdl.Quit()
	win := sdl.CreateWindow("Remoter self-test", 0, 0, 320, 240, sdlWindowHidden)
	if win == 0 {
		return fmt.Errorf("open window: %s", sdl.GetError())
	}
	ren := sdl.CreateRenderer(win, -1, 0)
	if ren == 0 {
		return fmt.Errorf("start renderer: %s", sdl.GetError())
	}
	tex := sdl.CreateTexture(ren, sdlPixelFormatIYUV, sdlTextureStreaming, 320, 240)
	if tex == 0 {
		return fmt.Errorf("video texture: %s", sdl.GetError())
	}

	newDec, err := videoDecoders()
	if err != nil {
		return err
	}
	if err := h264.Load(); err != nil {
		fmt.Printf("SDL and decoder load; no encoder here to test decoding (%v)\n", err)
		return nil
	}
	enc, err := h264.NewEncoder(320, 240, 1000, 30)
	if err != nil {
		return err
	}
	defer enc.Close()
	au, _, err := enc.Encode(image.NewRGBA(image.Rect(0, 0, 320, 240)), true)
	if err != nil {
		return err
	}
	dec, err := newDec()
	if err != nil {
		return err
	}
	defer dec.close()
	got, err := dec.decode(au)
	if err != nil {
		return err
	}
	if !got {
		return errors.New("key frame produced no picture")
	}
	p, err := dec.picture()
	if err != nil {
		return err
	}
	if p.w != 320 || p.h != 240 {
		return fmt.Errorf("decoded %dx%d, want 320x240", p.w, p.h)
	}
	if sdl.UpdateYUVTexture(tex, nil, p.planes[0], p.pitches[0], p.planes[1], p.pitches[1], p.planes[2], p.pitches[2]) != 0 {
		return fmt.Errorf("upload picture: %s", sdl.GetError())
	}
	return nil
}

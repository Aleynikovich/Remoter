//go:build windows

package main

import (
	"image"

	"github.com/aleynikovich/remoter/internal/capture"
	"github.com/aleynikovich/remoter/internal/host"
	"github.com/aleynikovich/remoter/internal/protocol"
	"github.com/aleynikovich/remoter/internal/wdesktop"
)

// followInputDesktop wraps the screen source and the input handler so each
// runs on a thread kept on the desktop that currently has input. The boot
// service starts the host as the system account outside any logged-in
// session, where capture and input would otherwise stay stuck on the empty
// Default desktop and never reach the sign-in or lock screen. Capture and
// input get separate threads so a slow capture never delays a keystroke.
func followInputDesktop(src capture.Source, in host.InputHandler) (capture.Source, host.InputHandler) {
	return followCapture{src: src, w: wdesktop.NewWorker("capture")},
		followInput{h: in, w: wdesktop.NewWorker("input")}
}

type followCapture struct {
	src capture.Source
	w   *wdesktop.Worker
}

func (f followCapture) Capture() (image.Image, error) {
	var img image.Image
	var err error
	f.w.Do(func() { img, err = f.src.Capture() })
	return img, err
}

type followInput struct {
	h host.InputHandler
	w *wdesktop.Worker
}

func (f followInput) HandleInput(viewer string, in protocol.Input) {
	f.w.Do(func() { f.h.HandleInput(viewer, in) })
}

// ReleaseAll forwards to the wrapped handler on the input thread, so the
// host's releaser check keeps working through the wrapper.
func (f followInput) ReleaseAll(viewer string) {
	if r, ok := f.h.(interface{ ReleaseAll(string) }); ok {
		f.w.Do(func() { r.ReleaseAll(viewer) })
	}
}

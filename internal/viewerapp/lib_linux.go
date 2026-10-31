package viewerapp

import (
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/ebitengine/purego"

	"github.com/aleynikovich/remoter/internal/h264"
)

// openSDL loads the system's SDL 2.
func openSDL() (func(name string) (uintptr, error), error) {
	for _, name := range []string{"libSDL2-2.0.so.0", "libSDL2-2.0.so", "libSDL2.so"} {
		if lib, err := purego.Dlopen(name, purego.RTLD_NOW|purego.RTLD_GLOBAL); err == nil {
			return func(name string) (uintptr, error) { return purego.Dlsym(lib, name) }, nil
		}
	}
	return nil, errors.New("SDL2 is not installed (Arch: pacman -S sdl2, Debian/Ubuntu: apt install libsdl2-2.0-0, Fedora: dnf install SDL2)")
}

// videoDecoders picks the H.264 decoder: FFmpeg, which decodes on several
// cores, or else OpenH264 if it is installed.
func videoDecoders() (func() (videoDecoder, error), error) {
	avErr := loadAV()
	if avErr == nil {
		return newAVDecoder, nil
	}
	if err := h264.Load(); err == nil {
		openH264Only = true
		return newOpenH264Decoder, nil
	}
	return nil, fmt.Errorf("%w; OpenH264 is not installed either", avErr)
}

// showError tells the user why the viewer cannot start, also when it was
// started from the menu rather than a terminal.
func showError(err error) {
	exec.Command("notify-send", "--app-name=Remoter", "Remoter cannot start", err.Error()).Run()
}

// setupLog copies the log to ~/.cache/remoter/viewer.log, since the
// viewer is usually started from the menu with nowhere to print.
func setupLog() {
	dir, err := os.UserCacheDir()
	if err != nil {
		return
	}
	dir = filepath.Join(dir, "remoter")
	os.MkdirAll(dir, 0o755)
	f, err := os.OpenFile(filepath.Join(dir, "viewer.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err == nil {
		log.SetOutput(io.MultiWriter(os.Stderr, f))
	}
}

// hideConsole is only needed on Windows.
func hideConsole() {}

package viewerapp

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/aleynikovich/remoter/internal/h264"
)

// SDL's official Windows build, fetched on first start and checked against
// this hash, so the viewer stays a single .exe.
const (
	sdlVersion   = "2.32.10"
	sdlZipURL    = "https://github.com/libsdl-org/SDL/releases/download/release-" + sdlVersion + "/SDL2-" + sdlVersion + "-win32-x64.zip"
	sdlZipSHA256 = "6cf9706eefd0a4a06dc764007934d428afaf029fabdd408a9e646048c91e18fb"
)

// openSDL loads SDL2.dll from $REMOTER_SDL2, next to the viewer, or the
// cache folder, downloading it there first if needed.
func openSDL() (func(name string) (uintptr, error), error) {
	path := os.Getenv("REMOTER_SDL2")
	if path == "" {
		if exe, err := os.Executable(); err == nil {
			if p := filepath.Join(filepath.Dir(exe), "SDL2.dll"); fileExists(p) {
				path = p
			}
		}
	}
	if path == "" {
		dir, err := os.UserCacheDir()
		if err != nil {
			return nil, err
		}
		dir = filepath.Join(dir, "Remoter")
		path = filepath.Join(dir, "SDL2-"+sdlVersion+".dll")
		if !fileExists(path) {
			if err := downloadSDL(dir, path); err != nil {
				return nil, fmt.Errorf("get SDL %s: %w", sdlVersion, err)
			}
		}
	}
	h, err := windows.LoadLibrary(path)
	if err != nil {
		return nil, fmt.Errorf("load %s: %w", path, err)
	}
	return func(name string) (uintptr, error) { return windows.GetProcAddress(h, name) }, nil
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func downloadSDL(dir, path string) error {
	log.Printf("downloading SDL %s (one time, about 600 KB)", sdlVersion)
	client := &http.Client{Timeout: 2 * time.Minute}
	resp, err := client.Get(sdlZipURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", sdlZipURL, resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return err
	}
	if sum := sha256.Sum256(data); hex.EncodeToString(sum[:]) != sdlZipSHA256 {
		return errors.New("download does not match the expected checksum")
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return err
	}
	for _, f := range zr.File {
		if f.Name != "SDL2.dll" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		defer rc.Close()
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		tmp, err := os.CreateTemp(dir, "SDL2.*.part")
		if err != nil {
			return err
		}
		defer os.Remove(tmp.Name())
		_, err = io.Copy(tmp, rc)
		if cerr := tmp.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
		return os.Rename(tmp.Name(), path)
	}
	return errors.New("SDL2.dll missing from the download")
}

// videoDecoders loads OpenH264, downloading it from Cisco the first time
// like the host does.
func videoDecoders() (func() (videoDecoder, error), error) {
	if err := h264.Load(); err != nil {
		return nil, err
	}
	openH264Only = true
	return newOpenH264Decoder, nil
}

// showError tells the user why the viewer cannot start. The viewer has no
// console on Windows, so it uses a message box.
func showError(err error) {
	text, _ := windows.UTF16PtrFromString(err.Error())
	title, _ := windows.UTF16PtrFromString("Remoter")
	windows.MessageBox(0, text, title, windows.MB_ICONERROR|windows.MB_OK)
}

// setupLog sends the log to %LocalAppData%\Remoter\viewer.log, since the
// viewer runs without a console window.
func setupLog() {
	dir, err := os.UserCacheDir()
	if err != nil {
		return
	}
	dir = filepath.Join(dir, "Remoter")
	os.MkdirAll(dir, 0o755)
	f, err := os.OpenFile(filepath.Join(dir, "viewer.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err == nil {
		log.SetOutput(f)
	}
}

var (
	kernel32                  = windows.NewLazySystemDLL("kernel32.dll")
	procGetConsoleProcessList = kernel32.NewProc("GetConsoleProcessList")
	procFreeConsole           = kernel32.NewProc("FreeConsole")
)

// hideConsole closes the console window Windows opens for Remoter when it
// is started from the desktop rather than from a command prompt: then this
// process is the only one using it.
func hideConsole() {
	var pids [2]uint32
	n, _, _ := procGetConsoleProcessList.Call(uintptr(unsafe.Pointer(&pids[0])), 2)
	if n == 1 {
		procFreeConsole.Call()
	}
}

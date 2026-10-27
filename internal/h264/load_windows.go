//go:build windows

package h264

import (
	"compress/bzip2"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Cisco distributes OpenH264 binaries and covers their H.264 patent license
// only when users download them from Cisco, so the DLL is fetched on first
// use instead of being shipped with Remoter.
const (
	dllName = "openh264-2.4.1-win64.dll"
	dllURL  = "https://ciscobinary.openh264.org/" + dllName + ".bz2"
)

// openLibrary loads the OpenH264 DLL from $REMOTER_OPENH264 or the cache
// folder, downloading it there first if needed.
func openLibrary() (func(name string) (uintptr, error), error) {
	if runtime.GOARCH != "amd64" {
		return nil, errors.New("h264: Cisco only publishes OpenH264 for 64-bit x86 Windows")
	}
	path := os.Getenv("REMOTER_OPENH264")
	if path == "" {
		dir, err := os.UserCacheDir()
		if err != nil {
			return nil, err
		}
		dir = filepath.Join(dir, "Remoter")
		path = filepath.Join(dir, dllName)
		if _, err := os.Stat(path); err != nil {
			if err := download(dir, path); err != nil {
				return nil, fmt.Errorf("h264: get %s: %w", dllName, err)
			}
		}
	}
	if err := verifySignature(path); err != nil {
		return nil, fmt.Errorf("h264: %s is not properly signed: %w", path, err)
	}
	h, err := windows.LoadLibrary(path)
	if err != nil {
		return nil, fmt.Errorf("h264: load %s: %w", path, err)
	}
	return func(name string) (uintptr, error) { return windows.GetProcAddress(h, name) }, nil
}

func download(dir, path string) error {
	log.Printf("downloading the OpenH264 video codec from Cisco (one time, about 1 MB)")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	client := &http.Client{Timeout: 2 * time.Minute}
	resp, err := client.Get(dllURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", dllURL, resp.Status)
	}
	tmp, err := os.CreateTemp(dir, dllName+".*.part")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	_, err = io.Copy(tmp, io.LimitReader(bzip2.NewReader(resp.Body), 32<<20))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if err := verifySignature(tmp.Name()); err != nil {
		return fmt.Errorf("downloaded file failed the signature check: %w", err)
	}
	return os.Rename(tmp.Name(), path)
}

// verifySignature checks the file's Authenticode signature, which Cisco puts
// on its OpenH264 builds.
func verifySignature(path string) error {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	file := &windows.WinTrustFileInfo{
		Size:     uint32(unsafe.Sizeof(windows.WinTrustFileInfo{})),
		FilePath: p,
	}
	data := &windows.WinTrustData{
		Size:                            uint32(unsafe.Sizeof(windows.WinTrustData{})),
		UIChoice:                        windows.WTD_UI_NONE,
		RevocationChecks:                windows.WTD_REVOKE_NONE,
		UnionChoice:                     windows.WTD_CHOICE_FILE,
		StateAction:                     windows.WTD_STATEACTION_VERIFY,
		FileOrCatalogOrBlobOrSgnrOrCert: unsafe.Pointer(file),
	}
	verr := windows.WinVerifyTrustEx(windows.InvalidHWND, &windows.WINTRUST_ACTION_GENERIC_VERIFY_V2, data)
	data.StateAction = windows.WTD_STATEACTION_CLOSE
	windows.WinVerifyTrustEx(windows.InvalidHWND, &windows.WINTRUST_ACTION_GENERIC_VERIFY_V2, data)
	runtime.KeepAlive(file)
	return verr
}

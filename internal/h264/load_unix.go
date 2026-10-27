//go:build linux || darwin || freebsd

package h264

import (
	"errors"
	"os"

	"github.com/ebitengine/purego"
)

// openLibrary loads libopenh264 from $REMOTER_OPENH264 or the system
// library path (on Debian and Ubuntu: apt install libopenh264-7) and returns
// its symbol lookup.
func openLibrary() (func(name string) (uintptr, error), error) {
	names := []string{"libopenh264.so.7", "libopenh264.so", "libopenh264.dylib"}
	if p := os.Getenv("REMOTER_OPENH264"); p != "" {
		names = append([]string{p}, names...)
	}
	for _, n := range names {
		h, err := purego.Dlopen(n, purego.RTLD_NOW|purego.RTLD_LOCAL)
		if err != nil {
			continue
		}
		if _, err := purego.Dlsym(h, "WelsCreateSVCEncoder"); err == nil {
			return func(name string) (uintptr, error) { return purego.Dlsym(h, name) }, nil
		}
	}
	return nil, errors.New("h264: libopenh264 not found (install libopenh264-7)")
}

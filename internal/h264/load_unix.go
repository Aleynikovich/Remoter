//go:build linux || darwin || freebsd

package h264

import (
	"errors"
	"os"

	"github.com/ebitengine/purego"
)

// openLibrary loads libopenh264 from $REMOTER_OPENH264 or the system
// library path (on Debian and Ubuntu: apt install libopenh264-7).
func openLibrary() (create, destroy uintptr, err error) {
	names := []string{"libopenh264.so.7", "libopenh264.so", "libopenh264.dylib"}
	if p := os.Getenv("REMOTER_OPENH264"); p != "" {
		names = append([]string{p}, names...)
	}
	for _, n := range names {
		h, err := purego.Dlopen(n, purego.RTLD_NOW|purego.RTLD_LOCAL)
		if err != nil {
			continue
		}
		c, err1 := purego.Dlsym(h, "WelsCreateSVCEncoder")
		d, err2 := purego.Dlsym(h, "WelsDestroySVCEncoder")
		if err1 == nil && err2 == nil {
			return c, d, nil
		}
	}
	return 0, 0, errors.New("h264: libopenh264 not found (install libopenh264-7)")
}

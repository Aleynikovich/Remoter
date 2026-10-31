//go:build windows

package capture

import (
	"testing"
	"time"
)

// TestScreen captures a few frames the way the host does. Whether desktop
// duplication or GDI is used depends on the machine; the log says which.
func TestScreen(t *testing.T) {
	if err := SelfTestDXGI(); err != nil {
		t.Logf("desktop duplication unavailable: %v", err)
	} else {
		EnableDXGI()
	}
	s := NewScreen(0)
	for i := range 5 {
		start := time.Now()
		img, err := s.Capture()
		if err != nil {
			t.Logf("capture %d: %v", i, err)
			continue
		}
		t.Logf("capture %d: %v in %v", i, img.Bounds(), time.Since(start))
		time.Sleep(50 * time.Millisecond)
	}
}

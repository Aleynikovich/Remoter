//go:build windows

package mf

import (
	"image"
	"strings"
	"testing"
)

// The CI runners have no GPU, but Windows' own software H.264 encoder goes
// through the same calls (minus the async events).
func TestSoftwareEncoder(t *testing.T) {
	name, err := selfTest(enumSyncMFT|enumSortAndFilt, []image.Point{{2400, 1350}, {1918, 1078}})
	if err != nil && strings.Contains(err.Error(), "no H.264 encoder") {
		t.Skip(err)
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("encoded with %s", name)
}

func TestHardwareEncoder(t *testing.T) {
	name, err := SelfTest()
	if err != nil {
		t.Skipf("no hardware encoder here: %v", err)
	}
	t.Logf("encoded with %s", name)
}

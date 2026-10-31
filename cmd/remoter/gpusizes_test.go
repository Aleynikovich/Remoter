package main

import (
	"image"
	"slices"
	"testing"
)

// The GPU self-test must try the sizes the stream really encodes: a
// 5120x1440 monitor goes out at most 4096 pixels across.
func TestGPUTestSizes(t *testing.T) {
	got := testSizes(image.Rect(0, 0, 5120, 1440))
	want := []string{"4096x1152", "3840x1080", "2560x720"}
	if !slices.Equal(got, want) {
		t.Fatalf("testSizes = %v, want %v", got, want)
	}
}

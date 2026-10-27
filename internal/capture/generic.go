//go:build !windows

package capture

func newNative(int) nativeSource { return nil }

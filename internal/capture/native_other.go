//go:build !windows && !linux

package capture

func newNative(int) nativeSource { return nil }

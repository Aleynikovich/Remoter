//go:build !windows && !linux

package clipboard

func newNative() Clipboard { return nil }

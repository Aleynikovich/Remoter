//go:build !windows

package clipboard

func newNative() Clipboard { return nil }

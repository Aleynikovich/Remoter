//go:build !windows

package capture

import "errors"

func newNative(int) nativeSource { return nil }

// EnableDXGI does nothing outside Windows.
func EnableDXGI() {}

// SelfTestDXGI reports that desktop duplication is Windows-only.
func SelfTestDXGI() error { return errors.New("desktop duplication needs Windows") }

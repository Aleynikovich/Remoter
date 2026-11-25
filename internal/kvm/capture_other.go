//go:build !windows

package kvm

func newCapture() (capture, error) { return nil, ErrUnsupported }

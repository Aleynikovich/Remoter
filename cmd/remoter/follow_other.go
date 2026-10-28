//go:build !windows

package main

import (
	"github.com/aleynikovich/remoter/internal/capture"
	"github.com/aleynikovich/remoter/internal/host"
)

// followInputDesktop is a no-op outside Windows: other systems have a single
// desktop, so capture and input need no help to reach the sign-in screen.
func followInputDesktop(src capture.Source, in host.InputHandler) (capture.Source, host.InputHandler) {
	return src, in
}

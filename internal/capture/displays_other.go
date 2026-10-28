//go:build !linux

package capture

import "image"

// Displays lists the bounds of each monitor, in the coordinates input uses.
func Displays() []image.Rectangle { return screenshotDisplays() }

//go:build !windows

package cursor

import "image"

func shape() string { return "" }

func draw(img image.Image, _ image.Point) image.Image { return img }

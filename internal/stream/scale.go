package stream

import (
	"image"
	"runtime"
	"sync"
)

// Downscale shrinks src to w×h by averaging the source pixels that fall on
// each destination pixel, reusing dst when it already has that size.
func Downscale(dst, src *image.RGBA, w, h int) *image.RGBA {
	if dst == nil || dst.Rect.Dx() != w || dst.Rect.Dy() != h {
		dst = image.NewRGBA(image.Rect(0, 0, w, h))
	}
	sw, sh := src.Rect.Dx(), src.Rect.Dy()
	// Source column span for each destination column.
	cols := make([]int, w+1)
	for x := range cols {
		cols[x] = x * sw / w
	}
	workers := min(runtime.GOMAXPROCS(0), 8)
	per := (h + workers - 1) / workers
	var wg sync.WaitGroup
	for y0 := 0; y0 < h; y0 += per {
		wg.Add(1)
		go func(y0, y1 int) {
			defer wg.Done()
			for y := y0; y < y1; y++ {
				sy0, sy1 := y*sh/h, max((y+1)*sh/h, y*sh/h+1)
				out := dst.Pix[y*dst.Stride : y*dst.Stride+w*4]
				for x := range w {
					sx0, sx1 := cols[x], max(cols[x+1], cols[x]+1)
					var r, g, b, n uint32
					for sy := sy0; sy < sy1; sy++ {
						row := src.Pix[sy*src.Stride+sx0*4 : sy*src.Stride+sx1*4]
						for i := 0; i+3 < len(row); i += 4 {
							r += uint32(row[i])
							g += uint32(row[i+1])
							b += uint32(row[i+2])
							n++
						}
					}
					o := out[x*4 : x*4+4 : x*4+4]
					o[0], o[1], o[2], o[3] = uint8(r/n), uint8(g/n), uint8(b/n), 255
				}
			}
		}(y0, min(y0+per, h))
	}
	wg.Wait()
	return dst
}

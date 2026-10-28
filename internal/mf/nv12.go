// Package mf encodes H.264 with a Media Foundation encoder on Windows,
// which is how Windows exposes the GPU's video encoder (NVIDIA NVENC, Intel
// Quick Sync, AMD VCE).
package mf

import (
	"image"
	"runtime"
	"sync"
)

// toNV12 converts the top-left w×h of img (w and h even) to NV12 in dst,
// which holds w*h*3/2 bytes: a full-size Y plane, then interleaved U and V
// at half size. Colours are BT.709 limited range, which is also what
// decoders assume for HD video that does not say.
func toNV12(dst []byte, img *image.RGBA, w, h int) {
	yp, uv := dst[:w*h], dst[w*h:w*h*3/2]
	parts := min(runtime.NumCPU(), h/2, 8)
	rowsPer := (h/2 + parts - 1) / parts * 2
	var wg sync.WaitGroup
	for y0 := 0; y0 < h; y0 += rowsPer {
		wg.Add(1)
		go func(y0, y1 int) {
			defer wg.Done()
			for y := y0; y < y1; y += 2 {
				r0 := img.Pix[y*img.Stride : y*img.Stride+w*4]
				r1 := img.Pix[(y+1)*img.Stride : (y+1)*img.Stride+w*4]
				y0row := yp[y*w : y*w+w]
				y1row := yp[(y+1)*w : (y+1)*w+w]
				c := uv[y/2*w : y/2*w+w]
				for x := 0; x+1 < w; x += 2 {
					i := x * 4
					a, b := r0[i:i+8:i+8], r1[i:i+8:i+8]
					ra, ga, ba := int32(a[0]), int32(a[1]), int32(a[2])
					rb, gb, bb := int32(a[4]), int32(a[5]), int32(a[6])
					rc, gc, bc := int32(b[0]), int32(b[1]), int32(b[2])
					rd, gd, bd := int32(b[4]), int32(b[5]), int32(b[6])
					y0row[x] = uint8((47*ra+157*ga+16*ba+128)>>8 + 16)
					y0row[x+1] = uint8((47*rb+157*gb+16*bb+128)>>8 + 16)
					y1row[x] = uint8((47*rc+157*gc+16*bc+128)>>8 + 16)
					y1row[x+1] = uint8((47*rd+157*gd+16*bd+128)>>8 + 16)
					sr, sg, sb := (ra+rb+rc+rd)>>2, (ga+gb+gc+gd)>>2, (ba+bb+bc+bd)>>2
					c[x] = uint8((-26*sr-86*sg+112*sb+128)>>8 + 128)
					c[x+1] = uint8((112*sr-102*sg-10*sb+128)>>8 + 128)
				}
			}
		}(y0, min(y0+rowsPer, h))
	}
	wg.Wait()
}

// nalTypes reports whether Annex B data holds an IDR slice and an SPS.
func nalTypes(data []byte) (idr, sps bool) {
	for i := 0; i+3 < len(data); i++ {
		if data[i] == 0 && data[i+1] == 0 && data[i+2] == 1 {
			switch data[i+3] & 0x1f {
			case 5:
				idr = true
			case 7:
				sps = true
			}
			i += 3
		}
	}
	return idr, sps
}

//go:build windows

package cursor

import (
	"image"
	"sync"
	"syscall"
	"unsafe"
)

var (
	gdi32            = syscall.NewLazyDLL("gdi32.dll")
	procGetIconInfo  = user32.NewProc("GetIconInfo")
	procGetDC        = user32.NewProc("GetDC")
	procReleaseDC    = user32.NewProc("ReleaseDC")
	procGetObject    = gdi32.NewProc("GetObjectW")
	procGetDIBits    = gdi32.NewProc("GetDIBits")
	procDeleteObject = gdi32.NewProc("DeleteObject")
)

// picture is a pointer shape ready to draw: straight-alpha BGRA pixels,
// plus pixels that invert what is under them (the I-beam and other
// monochrome pointers).
type picture struct {
	w, h, hotX, hotY int
	bgra             []byte
	invert           []bool
}

var (
	drawMu   sync.Mutex
	pictures = map[uintptr]*picture{}
	bufs     [2]*image.RGBA // returned in turn, like the capturers' own
	turn     int
)

func draw(img image.Image, origin image.Point) image.Image {
	src, ok := img.(*image.RGBA)
	if !ok || procGetCursorInfo.Find() != nil {
		return img
	}
	ci := cursorInfo{}
	ci.size = uint32(unsafe.Sizeof(ci))
	if ok, _, _ := procGetCursorInfo.Call(uintptr(unsafe.Pointer(&ci))); ok == 0 || ci.flags&cursorShowing == 0 || ci.handle == 0 {
		return img
	}
	drawMu.Lock()
	defer drawMu.Unlock()
	pic := pictures[ci.handle]
	if pic == nil {
		if pic = load(ci.handle); pic == nil {
			return img
		}
		if len(pictures) > 64 {
			clear(pictures)
		}
		pictures[ci.handle] = pic
	}
	// Top-left of the pointer in img's coordinates.
	b := src.Rect
	x0 := int(ci.x) - pic.hotX - origin.X + b.Min.X
	y0 := int(ci.y) - pic.hotY - origin.Y + b.Min.Y
	if !image.Rect(x0, y0, x0+pic.w, y0+pic.h).Overlaps(b) {
		return img
	}
	turn ^= 1
	dst := bufs[turn]
	if dst == nil || dst.Rect != b {
		dst = image.NewRGBA(b)
		bufs[turn] = dst
	}
	copy(dst.Pix, src.Pix)
	for j := 0; j < pic.h; j++ {
		y := y0 + j
		if y < b.Min.Y || y >= b.Max.Y {
			continue
		}
		for i := 0; i < pic.w; i++ {
			x := x0 + i
			if x < b.Min.X || x >= b.Max.X {
				continue
			}
			k := j*pic.w + i
			o := dst.PixOffset(x, y)
			d := dst.Pix[o : o+3 : o+3]
			if pic.invert[k] {
				d[0], d[1], d[2] = 255-d[0], 255-d[1], 255-d[2]
				continue
			}
			s := pic.bgra[4*k : 4*k+4 : 4*k+4]
			a := uint32(s[3])
			if a == 0 {
				continue
			}
			na := 255 - a
			d[0] = uint8((uint32(s[2])*a + uint32(d[0])*na + 127) / 255)
			d[1] = uint8((uint32(s[1])*a + uint32(d[1])*na + 127) / 255)
			d[2] = uint8((uint32(s[0])*a + uint32(d[2])*na + 127) / 255)
		}
	}
	return dst
}

type iconInfo struct {
	icon       int32
	hotX, hotY uint32
	mask       uintptr
	color      uintptr
}

type bitmap struct {
	typ, width, height, widthBytes int32
	planes, bitsPixel              uint16
	bits                           uintptr
}

type bitmapInfoHeader struct {
	size                   uint32
	width, height          int32
	planes, bitCount       uint16
	compression, sizeImage uint32
	xPels, yPels           int32
	clrUsed, clrImportant  uint32
	colors                 [4]uint32 // room for a palette, unused for 32 bits
}

// pixels reads a bitmap as top-down 32-bit BGRX.
func pixels(dc, hbm uintptr, w, h int) []byte {
	bi := bitmapInfoHeader{width: int32(w), height: -int32(h), planes: 1, bitCount: 32}
	bi.size = 40
	buf := make([]byte, 4*w*h)
	if n, _, _ := procGetDIBits.Call(dc, hbm, 0, uintptr(h), uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&bi)), 0); int(n) != h {
		return nil
	}
	return buf
}

// load turns a cursor handle into a picture.
func load(hc uintptr) *picture {
	var ii iconInfo
	if ok, _, _ := procGetIconInfo.Call(hc, uintptr(unsafe.Pointer(&ii))); ok == 0 {
		return nil
	}
	defer func() {
		if ii.mask != 0 {
			procDeleteObject.Call(ii.mask)
		}
		if ii.color != 0 {
			procDeleteObject.Call(ii.color)
		}
	}()
	var bm bitmap
	if ii.mask == 0 {
		return nil
	}
	if n, _, _ := procGetObject.Call(ii.mask, unsafe.Sizeof(bm), uintptr(unsafe.Pointer(&bm))); n == 0 {
		return nil
	}
	w, mh := int(bm.width), int(bm.height)
	h := mh
	if ii.color == 0 {
		h = mh / 2 // AND mask on top, XOR mask below
	}
	if w <= 0 || h <= 0 || w > 256 || h > 256 {
		return nil
	}
	dc, _, _ := procGetDC.Call(0)
	if dc == 0 {
		return nil
	}
	defer procReleaseDC.Call(0, dc)
	mask := pixels(dc, ii.mask, w, mh)
	if mask == nil {
		return nil
	}
	pic := &picture{w: w, h: h, hotX: int(ii.hotX), hotY: int(ii.hotY), bgra: make([]byte, 4*w*h), invert: make([]bool, w*h)}
	if ii.color == 0 {
		for k := 0; k < w*h; k++ {
			and := mask[4*k] != 0
			xor := mask[4*(w*h+k)] != 0
			switch {
			case and && xor:
				pic.invert[k] = true
			case !and:
				v := byte(0)
				if xor {
					v = 255
				}
				copy(pic.bgra[4*k:], []byte{v, v, v, 255})
			}
		}
		return pic
	}
	col := pixels(dc, ii.color, w, h)
	if col == nil {
		return nil
	}
	hasAlpha := false
	for k := 0; k < w*h; k++ {
		if col[4*k+3] != 0 {
			hasAlpha = true
			break
		}
	}
	for k := 0; k < w*h; k++ {
		copy(pic.bgra[4*k:4*k+3], col[4*k:4*k+3])
		switch {
		case hasAlpha:
			pic.bgra[4*k+3] = col[4*k+3]
		case mask[4*k] == 0:
			pic.bgra[4*k+3] = 255
		case col[4*k] != 0 || col[4*k+1] != 0 || col[4*k+2] != 0:
			// Mask set and colour not black: inverts, like the
			// monochrome I-beam.
			pic.invert[k] = true
		}
	}
	return pic
}

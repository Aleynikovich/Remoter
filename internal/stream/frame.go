// Package stream turns screen captures into compact screen updates and back.
//
// An update is one binary WebSocket message, little endian:
//
//	u8  'U'                 message kind
//	u32 seq                 increases by one per update
//	u16 width, height       full screen size in pixels
//	u16 count               number of rectangles that follow
//	count times:
//	  u16 x, y, w, h        where the rectangle goes on the screen
//	  u32 n                 length of the JPEG image
//	  n bytes               the JPEG image, exactly w×h pixels
//
// Viewers keep the last picture and draw each rectangle over it.
package stream

import (
	"encoding/binary"
	"errors"
	"image"
)

const kindUpdate = 'U'

// Rect is one changed area of the screen and its JPEG encoding.
type Rect struct {
	image.Rectangle
	JPEG []byte
}

// Update is a set of changed rectangles.
type Update struct {
	Seq    uint32
	Width  int
	Height int
	Rects  []Rect
}

// Full reports whether the update covers the whole screen, which is what a
// viewer that just joined needs before it can show anything.
func (u *Update) Full() bool {
	area := 0
	for _, r := range u.Rects {
		area += r.Dx() * r.Dy()
	}
	return area >= u.Width*u.Height
}

// Marshal encodes the update into buf, reusing its storage.
func (u *Update) Marshal(buf []byte) []byte {
	le := binary.LittleEndian
	buf = append(buf[:0], kindUpdate)
	buf = le.AppendUint32(buf, u.Seq)
	buf = le.AppendUint16(buf, uint16(u.Width))
	buf = le.AppendUint16(buf, uint16(u.Height))
	buf = le.AppendUint16(buf, uint16(len(u.Rects)))
	for _, r := range u.Rects {
		buf = le.AppendUint16(buf, uint16(r.Min.X))
		buf = le.AppendUint16(buf, uint16(r.Min.Y))
		buf = le.AppendUint16(buf, uint16(r.Dx()))
		buf = le.AppendUint16(buf, uint16(r.Dy()))
		buf = le.AppendUint32(buf, uint32(len(r.JPEG)))
		buf = append(buf, r.JPEG...)
	}
	return buf
}

// Parse decodes an update. The returned JPEG slices point into data.
func Parse(data []byte) (*Update, error) {
	le := binary.LittleEndian
	bad := errors.New("stream: malformed update")
	if len(data) < 11 || data[0] != kindUpdate {
		return nil, bad
	}
	u := &Update{
		Seq:    le.Uint32(data[1:]),
		Width:  int(le.Uint16(data[5:])),
		Height: int(le.Uint16(data[7:])),
	}
	n := int(le.Uint16(data[9:]))
	data = data[11:]
	for range n {
		if len(data) < 12 {
			return nil, bad
		}
		x, y := int(le.Uint16(data)), int(le.Uint16(data[2:]))
		w, h := int(le.Uint16(data[4:])), int(le.Uint16(data[6:]))
		size := int(le.Uint32(data[8:]))
		data = data[12:]
		if len(data) < size {
			return nil, bad
		}
		u.Rects = append(u.Rects, Rect{image.Rect(x, y, x+w, y+h), data[:size:size]})
		data = data[size:]
	}
	if len(data) != 0 {
		return nil, bad
	}
	return u, nil
}

package h264

import (
	"bytes"
	"image"
	"testing"
	"time"
)

// nalTypes lists the NAL unit types in an Annex B stream.
func nalTypes(data []byte) []byte {
	var types []byte
	for i := 0; i+3 < len(data); i++ {
		if data[i] == 0 && data[i+1] == 0 && data[i+2] == 1 {
			types = append(types, data[i+3]&0x1f)
			i += 3
		}
	}
	return types
}

func pattern(w, h, shift int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			i := img.PixOffset(x, y)
			img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = byte(x+shift), byte(y), byte(x^y), 255
		}
	}
	return img
}

func TestEncode(t *testing.T) {
	if err := Load(); err != nil {
		t.Skip(err)
	}
	e, err := NewEncoder(321, 241, 2000, 30)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if w, h := e.Size(); w != 320 || h != 240 {
		t.Fatalf("size %dx%d, want rounded down to 320x240", w, h)
	}
	data, key, err := e.Encode(pattern(321, 241, 0), false)
	if err != nil {
		t.Fatal(err)
	}
	types := nalTypes(data)
	if !key || !bytes.Contains(types, []byte{7}) || !bytes.Contains(types, []byte{8}) || !bytes.Contains(types, []byte{5}) {
		t.Fatalf("first frame: key=%v NAL types %v, want SPS, PPS and IDR", key, types)
	}
	data, key, err = e.Encode(pattern(321, 241, 4), false)
	if err != nil || key || len(data) == 0 {
		t.Fatalf("second frame: key=%v len=%d err=%v, want a P frame", key, len(data), err)
	}
	if _, key, _ = e.Encode(pattern(321, 241, 8), true); !key {
		t.Fatal("forced frame is not a key frame")
	}
}

func BenchmarkEncodeFullHD(b *testing.B) {
	if err := Load(); err != nil {
		b.Skip(err)
	}
	e, err := NewEncoder(1920, 1080, 8000, 30)
	if err != nil {
		b.Fatal(err)
	}
	defer e.Close()
	imgs := []*image.RGBA{pattern(1920, 1080, 0), pattern(1920, 1080, 3)}
	start := time.Now()
	bytes := 0
	for i := 0; b.Loop(); i++ {
		data, _, err := e.Encode(imgs[i%2], false)
		if err != nil {
			b.Fatal(err)
		}
		bytes += len(data)
	}
	b.ReportMetric(float64(bytes)/float64(b.N), "B/frame")
	_ = start
}

func BenchmarkI420(b *testing.B) {
	img := pattern(1920, 1080, 0)
	dst := make([]byte, 1920*1080*3/2)
	for b.Loop() {
		toI420(dst, img, 1920, 1080)
	}
}

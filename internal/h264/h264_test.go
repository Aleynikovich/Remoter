package h264

import (
	"bytes"
	"encoding/binary"
	"image"
	"os"
	"runtime"
	"testing"
	"time"
	"unsafe"
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

func TestDecodeRoundTrip(t *testing.T) {
	if err := Load(); err != nil {
		t.Skip(err)
	}
	e, err := NewEncoder(320, 240, 2000, 30)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	d, err := NewDecoder()
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	for i := range 3 {
		img := pattern(320, 240, i*4)
		data, _, err := e.Encode(img, false)
		if err != nil {
			t.Fatal(err)
		}
		p, err := d.Decode(data)
		if err != nil || p == nil {
			t.Fatalf("frame %d: picture %v, %v", i, p, err)
		}
		if p.Width != 320 || p.Height != 240 || p.Strides[0] < 320 {
			t.Fatalf("frame %d: %dx%d stride %d", i, p.Width, p.Height, p.Strides[0])
		}
		// Luma of a pixel in the middle should survive the trip roughly.
		got := *(*byte)(unsafe.Add(p.Planes[0], 120*p.Strides[0]+160))
		px := img.Pix[img.PixOffset(160, 120):]
		want := byte((66*int(px[0])+129*int(px[1])+25*int(px[2])+128)>>8 + 16) // BT.601 limited

		if diff := int(got) - int(want); diff < -24 || diff > 24 {
			t.Errorf("frame %d: luma %d, want about %d", i, got, want)
		}
	}
}

// TestRecordFirstFrame writes the first encoded frame to the file named by
// $H264_RECORD, to refresh testdata from a given OpenH264 version.
func TestRecordFirstFrame(t *testing.T) {
	path := os.Getenv("H264_RECORD")
	if path == "" {
		t.Skip("set H264_RECORD to record a first frame")
	}
	if err := Load(); err != nil {
		t.Fatal(err)
	}
	e, err := NewEncoder(320, 240, 500, 30)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	data, key, err := e.Encode(pattern(320, 240, 0), true)
	if err != nil || !key {
		t.Fatalf("key=%v err=%v", key, err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("OpenH264 %s: %d bytes, NAL types %v", Version(), len(data), nalTypes(data))
}

func TestSelfTest(t *testing.T) {
	if err := Load(); err != nil {
		t.Skip(err)
	}
	if err := SelfTest(); err != nil {
		t.Fatalf("OpenH264 %s: %v", Version(), err)
	}
}

// fakeFrameInfo builds an SFrameBSInfo in the given layout holding one
// layer with the NAL units nals, as OpenH264 would fill it in.
func fakeFrameInfo(lay infoLayout, typ uint32, nals [][]byte) (info []byte, keep any) {
	info = make([]byte, frameInfoSize)
	le := binary.LittleEndian
	le.PutUint32(info[0:], 1) // iLayerNum
	var buf []byte
	lens := make([]int32, len(nals))
	for i, n := range nals {
		buf = append(buf, n...)
		lens[i] = int32(len(n))
	}
	l := info[8:]
	le.PutUint32(l[16:], uint32(len(nals)))
	*(*unsafe.Pointer)(unsafe.Pointer(&l[24])) = unsafe.Pointer(&lens[0])
	*(*unsafe.Pointer)(unsafe.Pointer(&l[32])) = unsafe.Pointer(&buf[0])
	// Junk where the other layout keeps the frame type, so reading the
	// wrong offset shows.
	le.PutUint32(info[8+40*maxLayers:], 0xdead)
	le.PutUint32(info[8+56*maxLayers:], 0xdead)
	le.PutUint32(info[lay.frameTypeOff:], typ)
	return info, []any{lens, buf}
}

func TestFrameDataLayouts(t *testing.T) {
	first, err := os.ReadFile("testdata/openh264-2.7.0-first-frame.h264")
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range []struct{ major, minor uint32 }{{1, 8}, {2, 4}, {2, 5}, {2, 6}, {2, 7}, {3, 0}} {
		lay := layoutFor(v.major, v.minor)
		info, keep := fakeFrameInfo(lay, frameIDR, [][]byte{first[:len(first)/2], first[len(first)/2:]})
		data, typ := frameData(info, lay)
		runtime.KeepAlive(keep)
		if typ != frameIDR || !bytes.Equal(data, first) {
			t.Errorf("%d.%d: type %d, %d bytes; want IDR and the %d recorded bytes", v.major, v.minor, typ, len(data), len(first))
		}
		if err := checkKeyFrame(data, typ == frameIDR); err != nil {
			t.Errorf("%d.%d: %v", v.major, v.minor, err)
		}
	}
	if l := layoutFor(2, 7); l.layerSize != 56 || l.frameTypeOff != 7176 {
		t.Errorf("2.7 layout %+v, want 56-byte layers and the frame type at 7176", l)
	}
	if l := layoutFor(2, 4); l.layerSize != 40 || l.frameTypeOff != 5128 {
		t.Errorf("2.4 layout %+v, want 40-byte layers and the frame type at 5128", l)
	}
}

func TestCheckKeyFrame(t *testing.T) {
	first, err := os.ReadFile("testdata/openh264-2.7.0-first-frame.h264")
	if err != nil {
		t.Fatal(err)
	}
	if err := checkKeyFrame(first, true); err != nil {
		t.Errorf("recorded 2.7 frame: %v", err)
	}
	if types := nalTypes(first); !bytes.Contains(types, []byte{7}) || !bytes.Contains(types, []byte{8}) || !bytes.Contains(types, []byte{5}) {
		t.Errorf("recorded 2.7 frame NAL types %v, want SPS, PPS and IDR", types)
	}
	short := []byte{0, 0, 1, 0x67, 0x42, 0, 0, 1, 0x68, 0xce, 0, 0, 1, 0x65, 0x88}
	if err := checkKeyFrame(short, true); err != nil {
		t.Errorf("3-byte start codes: %v", err)
	}
	if checkKeyFrame(first, false) == nil {
		t.Error("frame not marked as key passed")
	}
	if checkKeyFrame([]byte{0, 0, 0, 1, 0x41, 0x9a}, true) == nil {
		t.Error("P slice passed as a key frame")
	}
	if checkKeyFrame(first[4:], true) == nil {
		t.Error("stream without a start code passed")
	}
}

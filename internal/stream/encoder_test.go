package stream

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"testing"
	"time"
)

// screen returns a 200x130 capture placed at an offset, like a secondary
// monitor, so the encoder has to work in screen-relative coordinates.
func screen() *image.RGBA {
	img := image.NewRGBA(image.Rect(1920, -40, 2120, 90))
	draw.Draw(img, img.Rect, image.NewUniform(color.RGBA{30, 60, 90, 255}), image.Point{}, draw.Src)
	return img
}

func encodeRoundTrip(t *testing.T, e *Encoder, img image.Image, full bool, now time.Time) *Update {
	t.Helper()
	u, err := e.Encode(img, full, now)
	if err != nil {
		t.Fatal(err)
	}
	if u == nil {
		return nil
	}
	got, err := Parse(u.Marshal(nil))
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range got.Rects {
		dec, err := jpeg.Decode(bytes.NewReader(r.JPEG))
		if err != nil {
			t.Fatal(err)
		}
		if dec.Bounds().Dx() != r.Dx() || dec.Bounds().Dy() != r.Dy() {
			t.Fatalf("rect %v holds a %v image", r.Rectangle, dec.Bounds())
		}
	}
	return got
}

func TestEncoderSendsOnlyChanges(t *testing.T) {
	e := &Encoder{Quality: 50, RefineQuality: 90, RefineAfter: time.Second}
	now := time.Now()
	img := screen()

	u := encodeRoundTrip(t, e, img, false, now)
	if u == nil || !u.Full() || u.Width != 200 || u.Height != 130 {
		t.Fatalf("first update should cover the 200x130 screen, got %+v", u)
	}
	// 200x130 is 4x3 tiles; every row becomes a single strip.
	if len(u.Rects) != 3 {
		t.Fatalf("got %d rects, want 3 strips", len(u.Rects))
	}

	if u := encodeRoundTrip(t, e, img, false, now); u != nil {
		t.Fatalf("unchanged screen produced %d rects", len(u.Rects))
	}

	img.Set(1920+70, -40+70, color.White) // tile (1,1)
	u = encodeRoundTrip(t, e, img, false, now)
	if u == nil || len(u.Rects) != 1 || u.Rects[0].Rectangle != image.Rect(64, 64, 128, 128) {
		t.Fatalf("want just tile (1,1), got %+v", u)
	}

	if u := encodeRoundTrip(t, e, img, true, now); u == nil || !u.Full() {
		t.Fatal("forced update should cover the screen")
	}
}

func TestEncoderRefinesStillTiles(t *testing.T) {
	e := &Encoder{Quality: 50, RefineQuality: 90, RefineAfter: time.Second}
	now := time.Now()
	img := screen()
	encodeRoundTrip(t, e, img, false, now)

	if u := encodeRoundTrip(t, e, img, false, now.Add(500*time.Millisecond)); u != nil {
		t.Fatal("refined too early")
	}
	u := encodeRoundTrip(t, e, img, false, now.Add(time.Second))
	if u == nil || !u.Full() {
		t.Fatalf("still screen should be refined once, got %+v", u)
	}
	if u := encodeRoundTrip(t, e, img, false, now.Add(2*time.Second)); u != nil {
		t.Fatal("refined twice")
	}
}

func TestEncoderResize(t *testing.T) {
	e := &Encoder{Quality: 50}
	encodeRoundTrip(t, e, screen(), false, time.Now())
	u := encodeRoundTrip(t, e, image.NewRGBA(image.Rect(0, 0, 100, 50)), false, time.Now())
	if u == nil || !u.Full() || u.Width != 100 {
		t.Fatalf("resize should send a full update, got %+v", u)
	}
}

func TestParseRejectsGarbage(t *testing.T) {
	u := &Update{Seq: 7, Width: 10, Height: 10, Rects: []Rect{{image.Rect(0, 0, 10, 10), []byte{1, 2, 3}}}}
	data := u.Marshal(nil)
	if _, err := Parse(data[:len(data)-1]); err == nil {
		t.Fatal("truncated update parsed")
	}
	got, err := Parse(data)
	if err != nil || got.Seq != 7 || len(got.Rects) != 1 {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func BenchmarkEncodeFullHD(b *testing.B) {
	img := image.NewRGBA(image.Rect(0, 0, 1920, 1080))
	for i := range img.Pix {
		img.Pix[i] = byte(i * 7 / 3)
	}
	e := &Encoder{Quality: 60}
	b.SetBytes(int64(len(img.Pix)))
	for b.Loop() {
		if _, err := e.Encode(img, true, time.Now()); err != nil {
			b.Fatal(err)
		}
	}
}

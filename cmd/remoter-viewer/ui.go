//go:build linux

package main

import (
	"image"
	"image/color"
	"image/draw"
	"strings"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

// The connect screen and notices are drawn in Go into an image and shown as
// one texture, so the viewer needs no toolkit beyond SDL.

var (
	colBackground = color.RGBA{0x1b, 0x1d, 0x23, 0xff}
	colPanel      = color.RGBA{0x26, 0x29, 0x32, 0xff}
	colField      = color.RGBA{0x14, 0x16, 0x1b, 0xff}
	colBorder     = color.RGBA{0x3a, 0x3e, 0x4a, 0xff}
	colFocus      = color.RGBA{0x4c, 0x8d, 0xff, 0xff}
	colButton     = color.RGBA{0x3b, 0x7b, 0xf6, 0xff}
	colText       = color.RGBA{0xe8, 0xea, 0xee, 0xff}
	colMuted      = color.RGBA{0x9a, 0xa0, 0xaa, 0xff}
	colError      = color.RGBA{0xff, 0x8a, 0x80, 0xff}
	colNotice     = color.RGBA{0x10, 0x12, 0x16, 0xe0}
)

var regular *opentype.Font

func init() {
	var err error
	if regular, err = opentype.Parse(goregular.TTF); err != nil {
		panic(err)
	}
}

// faces caches font faces by pixel size.
var faces = map[int]font.Face{}

func face(px int) font.Face {
	if f, ok := faces[px]; ok {
		return f
	}
	f, err := opentype.NewFace(regular, &opentype.FaceOptions{Size: float64(px), DPI: 72, Hinting: font.HintingFull})
	if err != nil {
		panic(err)
	}
	faces[px] = f
	return f
}

func textWidth(f font.Face, s string) int {
	return font.MeasureString(f, s).Ceil()
}

// drawText draws s with its baseline at y.
func drawText(img draw.Image, f font.Face, x, y int, s string, c color.Color) {
	d := font.Drawer{Dst: img, Src: image.NewUniform(c), Face: f, Dot: fixed.P(x, y)}
	d.DrawString(s)
}

func fill(img draw.Image, r image.Rectangle, c color.Color) {
	draw.Draw(img, r, image.NewUniform(c), image.Point{}, draw.Over)
}

// frame draws a one-pixel-per-unit border inside r.
func frame(img draw.Image, r image.Rectangle, width int, c color.Color) {
	fill(img, image.Rect(r.Min.X, r.Min.Y, r.Max.X, r.Min.Y+width), c)
	fill(img, image.Rect(r.Min.X, r.Max.Y-width, r.Max.X, r.Max.Y), c)
	fill(img, image.Rect(r.Min.X, r.Min.Y, r.Min.X+width, r.Max.Y), c)
	fill(img, image.Rect(r.Max.X-width, r.Min.Y, r.Max.X, r.Max.Y), c)
}

// form is the connect screen.
type form struct {
	labels [3]string
	values [3]string
	focus  int // 0..2 a field, 3 the Connect button
	status string
	err    bool

	// Where things were drawn, for clicks, in output pixels.
	hit [4]image.Rectangle
}

const fieldPassword = 2

func newForm(server, id, password string) *form {
	f := &form{labels: [3]string{"Server", "Computer ID (empty for the only one)", "Password"}}
	f.values = [3]string{server, id, password}
	// With everything remembered, Enter connects straight away.
	if server == "" {
		f.focus = 0
	} else {
		f.focus = 3
	}
	return f
}

// draw renders the connect screen for an output of size out, where s is the
// number of output pixels per window unit (2 on a 2x HiDPI screen).
func (f *form) draw(out image.Point, s float64) *image.RGBA {
	img := image.NewRGBA(image.Rectangle{Max: out})
	fill(img, img.Rect, colBackground)
	px := func(v float64) int { return int(v*s + 0.5) }

	title, body, small := face(px(28)), face(px(15)), face(px(13))
	w := min(px(440), out.X-px(32))
	fieldH, gap := px(38), px(14)
	h := px(120) + 3*(px(22)+fieldH+gap) + fieldH + px(70)
	panel := image.Rect((out.X-w)/2, max((out.Y-h)/2, px(16)), (out.X+w)/2, 0)
	panel.Max.Y = panel.Min.Y + h
	fill(img, panel, colPanel)

	x, y := panel.Min.X+px(28), panel.Min.Y+px(52)
	inner := w - px(56)
	drawText(img, title, x, y, "Remoter", colText)
	y += px(28)
	drawText(img, body, x, y, "Connect to a computer", colMuted)
	y += px(40)

	for i := range 3 {
		drawText(img, small, x, y, f.labels[i], colMuted)
		y += px(8)
		r := image.Rect(x, y, x+inner, y+fieldH)
		fill(img, r, colField)
		border, bw := colBorder, px(1)
		if f.focus == i {
			border, bw = colFocus, px(2)
		}
		frame(img, r, max(bw, 1), border)
		text := f.values[i]
		if i == fieldPassword {
			text = strings.Repeat("•", len([]rune(text)))
		}
		if f.focus == i {
			text += "|"
		}
		// Long values show their end, where typing happens.
		for textWidth(body, text) > inner-px(24) && len(text) > 0 {
			_, size := firstRune(text)
			text = text[size:]
		}
		drawText(img, body, x+px(12), y+fieldH/2+px(5), text, colText)
		f.hit[i] = r
		y += fieldH + gap + px(14)
	}

	y += px(4)
	btn := image.Rect(x, y, x+inner, y+fieldH)
	fill(img, btn, colButton)
	if f.focus == 3 {
		frame(img, btn, max(px(2), 1), colText)
	}
	label := "Connect"
	drawText(img, body, btn.Min.X+(inner-textWidth(body, label))/2, y+fieldH/2+px(5), label, colText)
	f.hit[3] = btn
	y += fieldH + px(28)

	if f.status != "" {
		c := colMuted
		if f.err {
			c = colError
		}
		drawText(img, small, x, y, f.status, c)
	}

	hint := "Tab: next field  ·  Enter: connect  ·  In a session, Ctrl+Alt+Esc releases keyboard and mouse"
	if textWidth(small, hint) > out.X-px(24) {
		hint = "Ctrl+Alt+Esc releases keyboard and mouse"
	}
	drawText(img, small, (out.X-textWidth(small, hint))/2, out.Y-px(18), hint, colMuted)
	return img
}

func firstRune(s string) (rune, int) {
	for _, r := range s {
		return r, len(string(r))
	}
	return 0, 0
}

// noticeImage renders a short message on a dark strip, for showing over the
// remote screen.
func noticeImage(text string, s float64) *image.RGBA {
	px := func(v float64) int { return int(v*s + 0.5) }
	f := face(px(15))
	r := image.Rect(0, 0, textWidth(f, text)+px(32), px(38))
	img := image.NewRGBA(r)
	fill(img, r, colNotice)
	drawText(img, f, px(16), px(25), text, colText)
	return img
}

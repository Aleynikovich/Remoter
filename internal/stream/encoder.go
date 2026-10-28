package stream

import (
	"bytes"
	"image"
	"image/draw"
	"image/jpeg"
	"runtime"
	"sync"
	"time"
)

// TileSize is the edge of the square tiles the screen is compared in.
const TileSize = 64

// Encoder compares each capture with the previous one and encodes only the
// tiles that changed. Changed tiles go out at Quality so motion stays smooth;
// once a tile has been still for RefineAfter it is sent once more at
// RefineQuality so text ends up sharp.
type Encoder struct {
	Quality       int
	RefineQuality int
	RefineAfter   time.Duration

	prev   []byte // last capture, tightly packed RGBA
	w, h   int
	tiles  []tileState
	encBuf sync.Pool
}

type tileState struct {
	changed time.Time
	refined bool
}

const (
	clean = iota
	dirty
	refine
)

// Encode returns the rectangles that differ from the previous capture, or
// every tile when full is set or the screen size changed. It returns nil when
// nothing needs sending.
func (e *Encoder) Encode(src image.Image, full bool, now time.Time) (*Update, error) {
	img := toRGBA(src)
	w, h := img.Rect.Dx(), img.Rect.Dy()
	cols, rows := (w+TileSize-1)/TileSize, (h+TileSize-1)/TileSize
	if w != e.w || h != e.h || e.prev == nil {
		e.w, e.h = w, h
		e.prev = make([]byte, w*h*4)
		e.tiles = make([]tileState, cols*rows)
		full = true
	}

	class := make([]uint8, cols*rows)
	for ty := range rows {
		for tx := range cols {
			i := ty*cols + tx
			t := &e.tiles[i]
			switch {
			case full || e.tileChanged(img, tx, ty):
				class[i] = dirty
				t.changed, t.refined = now, false
			case !t.refined && e.RefineQuality > e.Quality && now.Sub(t.changed) >= e.RefineAfter:
				class[i] = refine
				t.refined = true
			}
		}
	}

	// Merge runs of tiles in the same row and class into strips, so a full
	// screen is a few dozen JPEGs instead of hundreds.
	type job struct {
		r       image.Rectangle
		quality int
	}
	var jobs []job
	for ty := range rows {
		for tx := 0; tx < cols; {
			c := class[ty*cols+tx]
			if c == clean {
				tx++
				continue
			}
			start := tx
			for tx < cols && class[ty*cols+tx] == c {
				tx++
			}
			r := image.Rect(start*TileSize, ty*TileSize, tx*TileSize, (ty+1)*TileSize).Intersect(image.Rect(0, 0, w, h))
			q := e.Quality
			if c == refine {
				q = e.RefineQuality
			}
			jobs = append(jobs, job{r, q})
		}
	}
	if len(jobs) == 0 {
		return nil, nil
	}

	u := &Update{Width: w, Height: h, Rects: make([]Rect, len(jobs))}
	errs := make([]error, len(jobs))
	var wg sync.WaitGroup
	next := make(chan int)
	for range min(runtime.GOMAXPROCS(0), len(jobs)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			buf, _ := e.encBuf.Get().(*bytes.Buffer)
			if buf == nil {
				buf = new(bytes.Buffer)
			}
			defer e.encBuf.Put(buf)
			for i := range next {
				j := jobs[i]
				buf.Reset()
				sub := img.SubImage(j.r.Add(img.Rect.Min))
				errs[i] = jpeg.Encode(buf, sub, &jpeg.Options{Quality: j.quality})
				u.Rects[i] = Rect{j.r, bytes.Clone(buf.Bytes())}
			}
		}()
	}
	for i := range jobs {
		next <- i
	}
	close(next)
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}

	for y := range h {
		copy(e.prev[y*w*4:(y+1)*w*4], img.Pix[y*img.Stride:])
	}
	return u, nil
}

func (e *Encoder) tileChanged(img *image.RGBA, tx, ty int) bool {
	x0, x1 := tx*TileSize*4, min((tx+1)*TileSize, e.w)*4
	for y := ty * TileSize; y < min((ty+1)*TileSize, e.h); y++ {
		cur := img.Pix[y*img.Stride+x0 : y*img.Stride+x1]
		old := e.prev[y*e.w*4+x0 : y*e.w*4+x1]
		if !bytes.Equal(cur, old) {
			return true
		}
	}
	return false
}

func toRGBA(src image.Image) *image.RGBA {
	if img, ok := src.(*image.RGBA); ok {
		return img
	}
	img := image.NewRGBA(src.Bounds())
	draw.Draw(img, img.Rect, src, src.Bounds().Min, draw.Src)
	return img
}

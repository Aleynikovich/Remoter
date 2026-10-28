//go:build linux

package wayland

import (
	"errors"
	"fmt"
	"image"
	"runtime"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// wl_shm formats Remoter can read: little-endian 32-bit pixels.
const (
	formatARGB8888 = 0
	formatXRGB8888 = 1
	formatABGR8888 = 0x34324241
	formatXBGR8888 = 0x34324258
)

// Copier captures one output again and again with wlr-screencopy, reusing
// one shared-memory buffer.
type Copier struct {
	c      *Conn
	output uint32

	buf    uint32 // wl_buffer, 0 until the first capture
	pool   uint32
	mem    []byte
	format uint32
	w, h   int
	stride int

	imgs [2]*image.RGBA // returned in turn
	turn int
}

// NewCopier captures the output.
func (c *Conn) NewCopier(o Output) (*Copier, error) {
	if c.copier.id == 0 {
		return nil, errors.New("the compositor does not offer screen capture (wlr-screencopy)")
	}
	if c.shm == 0 {
		return nil, errors.New("the compositor offers no shared memory")
	}
	return &Copier{c: c, output: o.id}, nil
}

// Close frees the buffer.
func (p *Copier) Close() {
	p.release()
}

func (p *Copier) release() {
	if p.buf != 0 {
		p.c.send(p.buf, 0)  // wl_buffer.destroy
		p.c.send(p.pool, 1) // wl_shm_pool.destroy
		p.buf, p.pool = 0, 0
	}
	if p.mem != nil {
		unix.Munmap(p.mem)
		p.mem = nil
	}
}

type frameInfo struct {
	format, w, h, stride uint32
}

// Capture takes a screenshot of the output. The image is reused on the
// second call after the one that returned it.
func (p *Copier) Capture() (*image.RGBA, error) {
	c := p.c
	var (
		mu       sync.Mutex
		shm      []frameInfo
		yInvert  bool
		bufDone  = make(chan struct{}, 1)
		finished = make(chan error, 1)
	)
	frame := c.newID(func(op uint16, r *reader) {
		switch op {
		case 0: // buffer
			f := frameInfo{r.uint(), r.uint(), r.uint(), r.uint()}
			mu.Lock()
			shm = append(shm, f)
			mu.Unlock()
			if c.copier.Version < 3 {
				notify(bufDone, struct{}{})
			}
		case 1: // flags
			mu.Lock()
			yInvert = r.uint()&1 != 0
			mu.Unlock()
		case 2: // ready
			notify(finished, nil)
		case 3: // failed
			notify(finished, errors.New("the compositor could not capture the screen"))
		case 6: // buffer_done
			notify(bufDone, struct{}{})
		}
	})
	defer c.send(frame, 1)                                                    // zwlr_screencopy_frame_v1.destroy
	if err := c.send(c.copier.id, 0, frame, int32(0), p.output); err != nil { // capture_output, no cursor
		return nil, err
	}
	timeout := time.After(3 * time.Second)
	select {
	case <-bufDone:
	case err := <-finished:
		if err == nil {
			err = errors.New("capture ended early")
		}
		return nil, err
	case <-c.closed:
		return nil, c.Err()
	case <-timeout:
		return nil, errors.New("the compositor did not offer a capture buffer")
	}
	mu.Lock()
	var pick *frameInfo
	for i := range shm {
		switch shm[i].format {
		case formatARGB8888, formatXRGB8888, formatABGR8888, formatXBGR8888:
			pick = &shm[i]
		}
		if pick != nil {
			break
		}
	}
	mu.Unlock()
	if pick == nil {
		return nil, fmt.Errorf("the compositor offers no capture format Remoter reads (%v)", shm)
	}
	if err := p.ensureBuffer(*pick); err != nil {
		return nil, err
	}
	if err := c.send(frame, 0, p.buf); err != nil { // copy
		return nil, err
	}
	select {
	case err := <-finished:
		if err != nil {
			return nil, err
		}
	case <-c.closed:
		return nil, c.Err()
	case <-timeout:
		return nil, errors.New("the compositor did not finish the capture")
	}
	mu.Lock()
	flip := yInvert
	mu.Unlock()
	return p.convert(flip), nil
}

func (p *Copier) ensureBuffer(f frameInfo) error {
	w, h, stride := int(f.w), int(f.h), int(f.stride)
	if p.buf != 0 && p.w == w && p.h == h && p.stride == stride && p.format == f.format {
		return nil
	}
	p.release()
	size := stride * h
	memfd, err := unix.MemfdCreate("remoter-screencopy", unix.MFD_CLOEXEC)
	if err != nil {
		return err
	}
	defer unix.Close(memfd)
	if err := unix.Ftruncate(memfd, int64(size)); err != nil {
		return err
	}
	mem, err := unix.Mmap(memfd, 0, size, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
	if err != nil {
		return err
	}
	c := p.c
	pool := c.newID(nil)
	if err := c.send(c.shm, 0, pool, fd(memfd), int32(size)); err != nil { // wl_shm.create_pool
		unix.Munmap(mem)
		return err
	}
	buf := c.newID(nil)
	c.send(pool, 0, buf, int32(0), int32(w), int32(h), int32(stride), f.format) // create_buffer
	p.buf, p.pool, p.mem = buf, pool, mem
	p.w, p.h, p.stride, p.format = w, h, stride, f.format
	p.imgs = [2]*image.RGBA{image.NewRGBA(image.Rect(0, 0, w, h)), image.NewRGBA(image.Rect(0, 0, w, h))}
	return nil
}

// convert copies the buffer into the next RGBA image, on every core.
func (p *Copier) convert(flip bool) *image.RGBA {
	img := p.imgs[p.turn]
	p.turn ^= 1
	swap := p.format == formatARGB8888 || p.format == formatXRGB8888 // memory order B G R A
	parts := min(runtime.NumCPU(), p.h)
	var wg sync.WaitGroup
	for part := range parts {
		y0, y1 := p.h*part/parts, p.h*(part+1)/parts
		wg.Add(1)
		go func() {
			defer wg.Done()
			for y := y0; y < y1; y++ {
				sy := y
				if flip {
					sy = p.h - 1 - y
				}
				src := p.mem[sy*p.stride : sy*p.stride+p.w*4]
				dst := img.Pix[y*img.Stride : y*img.Stride+p.w*4]
				if swap {
					for i := 0; i < len(src); i += 4 {
						dst[i], dst[i+1], dst[i+2], dst[i+3] = src[i+2], src[i+1], src[i], 255
					}
				} else {
					for i := 0; i < len(src); i += 4 {
						dst[i], dst[i+1], dst[i+2], dst[i+3] = src[i], src[i+1], src[i+2], 255
					}
				}
			}
		}()
	}
	wg.Wait()
	return img
}

// notify sends without blocking, so a late or repeated event can never stall
// the connection's reader.
func notify[T any](ch chan T, v T) {
	select {
	case ch <- v:
	default:
	}
}

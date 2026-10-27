//go:build linux

package main

import (
	"errors"
	"fmt"
	"runtime"
	"strconv"
	"unsafe"

	"github.com/ebitengine/purego"
)

// The H.264 stream is decoded with the system's FFmpeg (libavcodec), loaded
// at run time like SDL. Only functions and the leading fields of AVFrame and
// AVPacket are used; those have not moved since FFmpeg 4, so any FFmpeg from
// 4 to 8 works.
const (
	avCodecIDH264  = 27
	avErrorEAGAIN  = -11
	avInputPadding = 64 // AV_INPUT_BUFFER_PADDING_SIZE
	avLogQuiet     = -8

	avPixFmtYUV420P  = 0
	avPixFmtYUVJ420P = 12

	// AVFrame: uint8_t *data[8]; int linesize[8]; uint8_t **extended_data;
	// int width, height, nb_samples, format.
	frameLinesize = 64
	frameWidth    = 104
	frameHeight   = 108
	frameFormat   = 116
	// AVPacket: AVBufferRef *buf; int64_t pts, dts; uint8_t *data; int size.
	packetData = 24
	packetSize = 32
)

var av struct {
	FindDecoder   func(id int32) uintptr
	AllocContext  func(codec uintptr) uintptr
	FreeContext   func(ctx *uintptr)
	Open          func(ctx, codec, opts uintptr) int32
	OptSet        func(obj uintptr, name, value string, flags int32) int32
	SendPacket    func(ctx, pkt uintptr) int32
	ReceiveFrame  func(ctx, frame uintptr) int32
	FlushBuffers  func(ctx uintptr)
	PacketAlloc   func() uintptr
	PacketFree    func(pkt *uintptr)
	FrameAlloc    func() uintptr
	FrameFree     func(frame *uintptr)
	FrameUnref    func(frame uintptr)
	FrameMoveRef  func(dst, src uintptr)
	Malloc        func(size uintptr) uintptr
	Free          func(p uintptr)
	ErrorString   func(err int32, buf *byte, size uintptr) int32
	LogSetLevel   func(level int32)
	versionLoaded string
}

func loadAV() error {
	var lib uintptr
	for _, major := range []string{"62", "61", "60", "59", "58"} {
		h, err := purego.Dlopen("libavcodec.so."+major, purego.RTLD_NOW|purego.RTLD_LOCAL)
		if err == nil {
			lib, av.versionLoaded = h, major
			break
		}
	}
	if lib == 0 {
		return errors.New("FFmpeg is not installed (Arch: pacman -S ffmpeg, Debian/Ubuntu: apt install libavcodec60)")
	}
	var err error
	bind := func(fn any, name string) {
		if err != nil {
			return
		}
		// dlsym on the libavcodec handle also finds libavutil's functions,
		// since it is a dependency.
		if _, e := purego.Dlsym(lib, name); e != nil {
			err = fmt.Errorf("libavcodec: %s missing", name)
			return
		}
		purego.RegisterLibFunc(fn, lib, name)
	}
	bind(&av.FindDecoder, "avcodec_find_decoder")
	bind(&av.AllocContext, "avcodec_alloc_context3")
	bind(&av.FreeContext, "avcodec_free_context")
	bind(&av.Open, "avcodec_open2")
	bind(&av.OptSet, "av_opt_set")
	bind(&av.SendPacket, "avcodec_send_packet")
	bind(&av.ReceiveFrame, "avcodec_receive_frame")
	bind(&av.FlushBuffers, "avcodec_flush_buffers")
	bind(&av.PacketAlloc, "av_packet_alloc")
	bind(&av.PacketFree, "av_packet_free")
	bind(&av.FrameAlloc, "av_frame_alloc")
	bind(&av.FrameFree, "av_frame_free")
	bind(&av.FrameUnref, "av_frame_unref")
	bind(&av.FrameMoveRef, "av_frame_move_ref")
	bind(&av.Malloc, "av_malloc")
	bind(&av.Free, "av_free")
	bind(&av.ErrorString, "av_strerror")
	bind(&av.LogSetLevel, "av_log_set_level")
	if err == nil {
		// Damaged frames are handled by asking for a key frame; FFmpeg's
		// own complaints about them would only fill the terminal.
		av.LogSetLevel(avLogQuiet)
	}
	return err
}

// decoder turns the host's H.264 access units into YUV pictures.
type decoder struct {
	ctx, pkt uintptr
	out      uintptr // receives pictures from the decoder
	frame    uintptr // the last complete picture
	buf      uintptr // packet data, with padding the decoder may read
	bufSize  int
}

func newDecoder() (*decoder, error) {
	codec := av.FindDecoder(avCodecIDH264)
	if codec == 0 {
		return nil, errors.New("this FFmpeg has no H.264 decoder")
	}
	d := &decoder{ctx: av.AllocContext(codec), pkt: av.PacketAlloc(), out: av.FrameAlloc(), frame: av.FrameAlloc()}
	if d.ctx == 0 || d.pkt == 0 || d.out == 0 || d.frame == 0 {
		d.close()
		return nil, errors.New("libavcodec: out of memory")
	}
	// Slice threads decode a frame on several cores without holding frames
	// back the way frame threads do, and low_delay outputs each picture as
	// soon as it is complete. The host sends several slices per frame and
	// no B-frames, so nothing is lost.
	threads := strconv.Itoa(min(runtime.NumCPU(), 8))
	for _, o := range [][2]string{{"threads", threads}, {"thread_type", "slice"}, {"flags", "+low_delay"}, {"flags2", "+fast"}} {
		av.OptSet(d.ctx, o[0], o[1], 0)
	}
	if rc := av.Open(d.ctx, codec, 0); rc < 0 {
		d.close()
		return nil, fmt.Errorf("open H.264 decoder: %s", avError(rc))
	}
	return d, nil
}

func (d *decoder) close() {
	if d.ctx != 0 {
		av.FreeContext(&d.ctx)
	}
	if d.pkt != 0 {
		av.PacketFree(&d.pkt)
	}
	if d.out != 0 {
		av.FrameFree(&d.out)
	}
	if d.frame != 0 {
		av.FrameFree(&d.frame)
	}
	if d.buf != 0 {
		av.Free(d.buf)
		d.buf = 0
	}
}

// decode feeds one access unit and reports whether it completed a picture,
// which picture() then describes until the next call.
func (d *decoder) decode(au []byte) (bool, error) {
	if len(au)+avInputPadding > d.bufSize {
		if d.buf != 0 {
			av.Free(d.buf)
		}
		d.bufSize = (len(au) + avInputPadding) * 2
		d.buf = av.Malloc(uintptr(d.bufSize))
		if d.buf == 0 {
			d.bufSize = 0
			return false, errors.New("libavcodec: out of memory")
		}
	}
	dst := unsafe.Slice((*byte)(cPointer(d.buf)), d.bufSize)
	copy(dst, au)
	clear(dst[len(au) : len(au)+avInputPadding])
	pkt := cPointer(d.pkt)
	*(*uintptr)(unsafe.Add(pkt, packetData)) = d.buf
	*(*int32)(unsafe.Add(pkt, packetSize)) = int32(len(au))
	rc := av.SendPacket(d.ctx, d.pkt)
	*(*uintptr)(unsafe.Add(pkt, packetData)) = 0
	*(*int32)(unsafe.Add(pkt, packetSize)) = 0
	if rc < 0 && rc != avErrorEAGAIN {
		return false, fmt.Errorf("decode: %s", avError(rc))
	}
	got := false
	for {
		// receive_frame empties out even when it has nothing, so a
		// picture is moved aside as soon as it arrives.
		rc := av.ReceiveFrame(d.ctx, d.out)
		if rc == avErrorEAGAIN {
			break
		}
		if rc < 0 {
			return got, fmt.Errorf("decode: %s", avError(rc))
		}
		av.FrameUnref(d.frame)
		av.FrameMoveRef(d.frame, d.out)
		got = true
	}
	return got, nil
}

// yuvPicture points at the planes of the last decoded picture.
type yuvPicture struct {
	w, h    int
	planes  [3]unsafe.Pointer
	pitches [3]int32
}

func (d *decoder) picture() (yuvPicture, error) {
	f := cPointer(d.frame)
	if format := *(*int32)(unsafe.Add(f, frameFormat)); format != avPixFmtYUV420P && format != avPixFmtYUVJ420P {
		return yuvPicture{}, fmt.Errorf("unexpected picture format %d", format)
	}
	p := yuvPicture{
		w: int(*(*int32)(unsafe.Add(f, frameWidth))),
		h: int(*(*int32)(unsafe.Add(f, frameHeight))),
	}
	for i := range 3 {
		p.planes[i] = *(*unsafe.Pointer)(unsafe.Add(f, i*8))
		p.pitches[i] = *(*int32)(unsafe.Add(f, frameLinesize+i*4))
	}
	return p, nil
}

// reset drops any half-decoded state, after the stream restarts.
func (d *decoder) reset() { av.FlushBuffers(d.ctx) }

func avError(rc int32) string {
	buf := make([]byte, 128)
	if av.ErrorString(rc, &buf[0], uintptr(len(buf))) < 0 {
		return fmt.Sprintf("error %d", rc)
	}
	for i, b := range buf {
		if b == 0 {
			return string(buf[:i])
		}
	}
	return string(buf)
}

package main

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"image"
	"log"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aleynikovich/remoter/internal/capture"
	"github.com/aleynikovich/remoter/internal/clipboard"
	"github.com/aleynikovich/remoter/internal/cursor"
	"github.com/aleynikovich/remoter/internal/diag"
	"github.com/aleynikovich/remoter/internal/h264"
	"github.com/aleynikovich/remoter/internal/host"
	"github.com/aleynikovich/remoter/internal/input"
	"github.com/aleynikovich/remoter/internal/mf"
	"github.com/aleynikovich/remoter/internal/update"
)

type hostOptions struct {
	server      string
	password    string
	id          string
	fps         int
	quality     int
	display     int
	test        bool
	viewOnly    bool
	noClipboard bool
	noVideo     bool
	kbps        int
}

func newAgent(o hostOptions) *host.Agent {
	screen := capture.NewScreen(o.display)
	// Viewers draw their own pointer for speed; this one shows where the
	// real pointer is.
	screen.Overlay = cursor.Draw
	if runtime.GOOS == "windows" && !o.test {
		go func() {
			if err := childSelfTest("-capture-selftest"); err != nil {
				log.Printf("fast screen capture off, using GDI: %v", err)
				return
			}
			capture.EnableDXGI()
		}()
	}
	var src capture.Source = screen
	var in input.Handler = input.Log{}
	var mons host.Monitors
	if o.test {
		src = capture.TestPattern{}
	} else {
		if !o.viewOnly {
			native, err := input.New(displayBounds(o.display))
			if err != nil {
				log.Printf("remote control disabled: %v", err)
			} else {
				in = native
			}
		}
		mons = monitors{screen, in}
	}
	var clip clipboard.Clipboard
	if !o.noClipboard && !o.viewOnly {
		clip = clipboard.New()
	}
	if o.password == "" {
		o.password = host.RandomPassword()
	}

	shown := ""
	a := &host.Agent{
		ServerURL: o.server,
		Password:  o.password,
		ID:        o.id,
		Source:    src,
		Input:     in,
		FPS:       o.fps,
		Quality:   o.quality,
		Clipboard: clip,
		Cursor:    cursor.Shape,
		Monitors:  mons,
		Diagnostics: func() string {
			enc, _ := videoEncoder.Load().(string)
			if enc == "" {
				enc = "none yet"
			}
			fps := "no limit"
			if o.fps > 0 {
				fps = fmt.Sprint(o.fps)
			}
			return fmt.Sprintf("Remoter %s on %s/%s, %d CPUs, administrator %v\nsettings: fps %s, bitrate %d kbit/s, JPEG quality %d, display %d, video off %v\nvideo encoder: %s\n--- recent log ---\n%s",
				version(), runtime.GOOS, runtime.GOARCH, runtime.NumCPU(), isAdmin(), fps, o.kbps, o.quality, o.display, o.noVideo, enc, diag.Log)
		},
		OnRegistered: func(id string) {
			if id == shown {
				return
			}
			shown = id
			fmt.Printf("\n  Your ID:   %s %s %s\n  Password:  %s\n\n", id[:3], id[3:6], id[6:], o.password)
		},
	}
	if !o.noVideo {
		// Loading may download the codec, so do it in the background and
		// send JPEG until it is ready.
		var status atomic.Value
		status.Store("the host is still loading the H.264 codec")
		go func() {
			if err := prepareVideo(); err != nil {
				log.Printf("H.264 video off, sending JPEG: %v", err)
				status.Store("the host could not load H.264: " + err.Error())
				return
			}
			log.Print("H.264 video ready")
			status.Store("")
		}()
		a.VideoStatus = func() string { return status.Load().(string) }
		cpu := func(w, h, kbps int) (host.VideoEncoder, error) {
			e, err := h264.NewEncoder(w, h, cmp.Or(kbps, o.kbps), o.fps)
			if err == nil {
				videoEncoder.Store("OpenH264 on the CPU")
			}
			return e, err
		}
		a.NewVideo = func(w, h, kbps int) (host.VideoEncoder, error) {
			if !gpuVideo.Load() {
				return cpu(w, h, kbps)
			}
			size := image.Pt(w, h)
			if _, failed := gpuFailed.Load(size); failed {
				return cpu(w, h, kbps)
			}
			e, err := mf.NewEncoder(w, h, cmp.Or(kbps, o.kbps))
			if err != nil {
				// Only this size: Auto may well pick one the GPU takes.
				log.Printf("GPU H.264 encoder cannot do %dx%d, using the CPU for it: %v", w, h, err)
				gpuFailed.Store(size, true)
				return cpu(w, h, kbps)
			}
			if videoEncoder.Swap(e.Name()+" on the GPU") != e.Name()+" on the GPU" {
				log.Printf("H.264 encoding on the GPU with %s", e.Name())
			}
			return &fallbackVideo{VideoEncoder: e, fallback: func(w, h int) (host.VideoEncoder, error) { return cpu(w, h, kbps) }}, nil
		}
		if runtime.GOOS == "windows" {
			go func() {
				// Try the sizes the stream will use here, too: each
				// monitor's and all of them side by side, at 100% and
				// at sizes Auto picks, as the stream limits them.
				sizes := []string{"1280x720"}
				var all image.Rectangle
				for _, r := range capture.Displays() {
					all = all.Union(r)
					sizes = append(sizes, testSizes(r)...)
				}
				if !all.Empty() {
					sizes = append(sizes, testSizes(all)...)
				}
				out, err := childSelfTestOutput("-gpu-video-selftest=" + strings.Join(sizes, ","))
				if err != nil {
					log.Printf("GPU H.264 encoder off, using the CPU: %v", err)
					return
				}
				lines := strings.Split(strings.TrimSpace(string(out)), "\n")
				log.Printf("GPU H.264 encoder %s passed its self-test", strings.TrimSpace(lines[0]))
				for _, l := range lines[1:] {
					var size image.Point
					if _, err := fmt.Sscanf(l, "refused %dx%d:", &size.X, &size.Y); err == nil {
						gpuFailed.Store(size, true)
						log.Printf("GPU H.264 encoder %s; the CPU encodes that size", strings.TrimSpace(l))
					}
				}
				gpuVideo.Store(true)
				// Switch encoders on the next picture.
				a.RestartVideo()
			}()
		}
	}
	return a
}

// prepareVideo loads the H.264 codec and tries it in a child process first,
// so a codec that does not work here cannot take the host down with it.
func prepareVideo() error {
	if err := h264.Load(); err != nil {
		return err
	}
	return childSelfTest("-h264-selftest")
}

// childSelfTest runs this program with flag in a child process, which exits
// with an error (or crashes) when the feature does not work here.
func childSelfTest(flag string) error {
	_, err := childSelfTestOutput(flag)
	return err
}

// childSelfTestOutput is childSelfTest that also returns what the child
// printed.
func childSelfTestOutput(flag string) ([]byte, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, flag)
	update.HideWindow(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		first, _, _ := bytes.Cut(bytes.TrimSpace(out), []byte("\n"))
		return nil, fmt.Errorf("self-test failed: %v: %s", err, first)
	}
	return out, nil
}

// runHost keeps the agent connected to the relay until ctx ends, so the
// host survives the relay restarting (after an update, say).
func runHost(ctx context.Context, a *host.Agent) {
	connected := true
	for ctx.Err() == nil {
		start := time.Now()
		err := a.Run(ctx)
		if ctx.Err() != nil {
			return
		}
		if connected || time.Since(start) > 10*time.Second {
			log.Printf("relay connection lost (%v), reconnecting", err)
		}
		connected = time.Since(start) > 10*time.Second
		select {
		case <-ctx.Done():
		case <-time.After(time.Second):
		}
	}
}

// monitors switches both screen capture and input to another display. The
// index one past the last display shows all of them at once.
type monitors struct {
	screen *capture.Screen
	input  input.Handler
}

func (m monitors) Count() int { return len(capture.Displays()) }

func (m monitors) Current() int {
	if d := m.screen.Display(); d != capture.All {
		return d
	}
	return m.Count()
}

func (m monitors) Select(display int) error {
	n := len(capture.Displays())
	if display < 0 || display > n || display == n && n < 2 {
		return errors.New("no such display")
	}
	if display == n {
		display = capture.All
	}
	m.input.SetDisplay(displayBounds(display))
	m.screen.SetDisplay(display)
	return nil
}

// displayBounds is the area of the display (or of every display, for
// capture.All) in input coordinates, or an empty rectangle when it is not
// there (yet: a service can start before the desktop).
func displayBounds(display int) image.Rectangle {
	ds := capture.Displays()
	if display == capture.All {
		return capture.Union(ds)
	}
	if display < len(ds) {
		return ds[display]
	}
	return image.Rectangle{}
}

var (
	// gpuVideo is set once the GPU's H.264 encoder passed its self-test,
	// and cleared when it fails.
	gpuVideo atomic.Bool
	// videoEncoder names the encoder in use, for diagnostics.
	videoEncoder atomic.Value
)

// fallbackVideo encodes on the GPU until that fails, then on the CPU.
type fallbackVideo struct {
	host.VideoEncoder
	fallback func(w, h int) (host.VideoEncoder, error)
	cpu      bool
}

func (f *fallbackVideo) Encode(img *image.RGBA, key bool) ([]byte, bool, error) {
	data, isKey, err := f.VideoEncoder.Encode(img, key)
	if err == nil || f.cpu {
		return data, isKey, err
	}
	log.Printf("GPU H.264 encoder failed, using the CPU: %v", err)
	gpuVideo.Store(false)
	w, h := f.Size()
	f.VideoEncoder.Close()
	f.cpu = true
	if f.VideoEncoder, err = f.fallback(w, h); err != nil {
		f.VideoEncoder = closedVideo{w, h}
		return nil, false, err
	}
	return f.VideoEncoder.Encode(img, true)
}

// closedVideo stands in for an encoder that could not be opened.
type closedVideo struct{ w, h int }

func (c closedVideo) Size() (int, int) { return c.w, c.h }
func (c closedVideo) Close()           {}
func (c closedVideo) Encode(*image.RGBA, bool) ([]byte, bool, error) {
	return nil, false, errors.New("no video encoder")
}

// gpuFailed holds the picture sizes the GPU encoder refused.
var gpuFailed sync.Map

// testSizes lists, as WxH, the sizes video of a capture of r is encoded at
// at full resolution and two of Auto's steps.
func testSizes(r image.Rectangle) []string {
	var sizes []string
	for _, scale := range []int{100, 75, 50} {
		w, h := host.VideoSize(r.Dx(), r.Dy(), scale)
		if s := fmt.Sprintf("%dx%d", w&^1, h&^1); !slices.Contains(sizes, s) {
			sizes = append(sizes, s)
		}
	}
	return sizes
}

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"runtime"
	"sync/atomic"
	"time"

	"github.com/kbinani/screenshot"

	"github.com/aleynikovich/remoter/internal/capture"
	"github.com/aleynikovich/remoter/internal/clipboard"
	"github.com/aleynikovich/remoter/internal/cursor"
	"github.com/aleynikovich/remoter/internal/diag"
	"github.com/aleynikovich/remoter/internal/h264"
	"github.com/aleynikovich/remoter/internal/host"
	"github.com/aleynikovich/remoter/internal/input"
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
			native, err := input.New(screenshot.GetDisplayBounds(o.display))
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
			return fmt.Sprintf("Remoter %s on %s/%s, %d CPUs, administrator %v\nsettings: fps %d, bitrate %d kbit/s, JPEG quality %d, display %d, video off %v\n--- recent log ---\n%s",
				version(), runtime.GOOS, runtime.GOARCH, runtime.NumCPU(), isAdmin(), o.fps, o.kbps, o.quality, o.display, o.noVideo, diag.Log)
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
		a.NewVideo = func(w, h int) (host.VideoEncoder, error) {
			return h264.NewEncoder(w, h, o.kbps, o.fps)
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
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, flag)
	update.HideWindow(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		first, _, _ := bytes.Cut(bytes.TrimSpace(out), []byte("\n"))
		return fmt.Errorf("self-test failed: %v: %s", err, first)
	}
	return nil
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

// monitors switches both screen capture and input to another display.
type monitors struct {
	screen *capture.Screen
	input  input.Handler
}

func (m monitors) Count() int   { return screenshot.NumActiveDisplays() }
func (m monitors) Current() int { return m.screen.Display() }

func (m monitors) Select(display int) error {
	if display < 0 || display >= screenshot.NumActiveDisplays() {
		return errors.New("no such display")
	}
	m.input.SetDisplay(screenshot.GetDisplayBounds(display))
	m.screen.SetDisplay(display)
	return nil
}

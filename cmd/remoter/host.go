package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/kbinani/screenshot"

	"github.com/aleynikovich/remoter/internal/capture"
	"github.com/aleynikovich/remoter/internal/clipboard"
	"github.com/aleynikovich/remoter/internal/cursor"
	"github.com/aleynikovich/remoter/internal/host"
	"github.com/aleynikovich/remoter/internal/input"
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
}

func newAgent(o hostOptions) *host.Agent {
	screen := capture.NewScreen(o.display)
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
	return &host.Agent{
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
		OnRegistered: func(id string) {
			if id == shown {
				return
			}
			shown = id
			fmt.Printf("\n  Your ID:   %s %s %s\n  Password:  %s\n\n", id[:3], id[3:6], id[6:], o.password)
		},
	}
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

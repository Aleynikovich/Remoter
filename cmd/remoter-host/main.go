// Command remoter-host shares this machine's screen through a relay server.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"

	"github.com/kbinani/screenshot"

	"github.com/aleynikovich/remoter/internal/capture"
	"github.com/aleynikovich/remoter/internal/clipboard"
	"github.com/aleynikovich/remoter/internal/cursor"
	"github.com/aleynikovich/remoter/internal/host"
	"github.com/aleynikovich/remoter/internal/input"
)

func main() {
	server := flag.String("server", "ws://localhost:8080/ws/host", "relay host endpoint")
	password := flag.String("password", "", "session password (random if empty)")
	fps := flag.Int("fps", 30, "maximum frames per second")
	quality := flag.Int("quality", 60, "JPEG quality 1-100 for changing areas (still areas are resent sharper)")
	display := flag.Int("display", 0, "display index to share")
	test := flag.Bool("test-pattern", false, "stream a generated test pattern instead of the screen")
	viewOnly := flag.Bool("view-only", false, "let viewers watch but not control this computer")
	noClipboard := flag.Bool("no-clipboard", false, "do not share the clipboard with viewers")
	flag.Parse()

	screen := capture.NewScreen(*display)
	var src capture.Source = screen
	var in input.Handler = input.Log{}
	var mons host.Monitors
	if *test {
		src = capture.TestPattern{}
	} else {
		if !*viewOnly {
			native, err := input.New(screenshot.GetDisplayBounds(*display))
			if err != nil {
				log.Printf("remote control disabled: %v", err)
			} else {
				in = native
			}
		}
		mons = monitors{screen, in}
	}
	var clip clipboard.Clipboard
	if !*noClipboard && !*viewOnly {
		clip = clipboard.New()
	}
	if *password == "" {
		*password = host.RandomPassword()
	}

	a := &host.Agent{
		ServerURL: *server,
		Password:  *password,
		Source:    src,
		Input:     in,
		FPS:       *fps,
		Quality:   *quality,
		Clipboard: clip,
		Cursor:    cursor.Shape,
		Monitors:  mons,
		OnRegistered: func(id string) {
			fmt.Printf("\n  Your ID:   %s %s %s\n  Password:  %s\n\n", id[:3], id[3:6], id[6:], *password)
		},
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := a.Run(ctx); err != nil && ctx.Err() == nil {
		log.Fatal(err)
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

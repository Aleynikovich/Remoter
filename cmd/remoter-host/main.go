// Command remoter-host shares this machine's screen through a relay server.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"

	"github.com/kbinani/screenshot"

	"github.com/aleynikovich/remoter/internal/capture"
	"github.com/aleynikovich/remoter/internal/clipboard"
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

	var src capture.Source = &capture.Screen{Display: *display}
	var in host.InputHandler = input.Log{}
	if *test {
		src = capture.TestPattern{}
	} else if !*viewOnly {
		native, err := input.New(screenshot.GetDisplayBounds(*display))
		if err != nil {
			log.Printf("remote control disabled: %v", err)
		} else {
			in = native
		}
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

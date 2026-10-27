// Command remoter-host shares this machine's screen through a relay server.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"

	"github.com/aleynikovich/remoter/internal/capture"
	"github.com/aleynikovich/remoter/internal/host"
)

func main() {
	server := flag.String("server", "ws://localhost:8080/ws/host", "relay host endpoint")
	password := flag.String("password", "", "session password (random if empty)")
	fps := flag.Int("fps", 10, "frames per second")
	quality := flag.Int("quality", 70, "JPEG quality 1-100")
	display := flag.Int("display", 0, "display index to share")
	test := flag.Bool("test-pattern", false, "stream a generated test pattern instead of the screen")
	flag.Parse()

	var src capture.Source = capture.Screen{Display: *display}
	if *test {
		src = capture.TestPattern{}
	}
	if *password == "" {
		*password = host.RandomPassword()
	}

	a := &host.Agent{
		ServerURL: *server,
		Password:  *password,
		Source:    src,
		FPS:       *fps,
		Quality:   *quality,
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

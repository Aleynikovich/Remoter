//go:build linux || windows

// Command remoter-viewer is the desktop viewer for Linux and Windows: a native window
// that shows a Remoter host's screen and hands it the keyboard and mouse,
// desktop shortcuts included.
//
//	remoter-viewer [-password root] [-id 123456789] [-fullscreen] [server:port]
//	remoter-viewer phone [ip]       mirror and control an Android phone
//	remoter-viewer phone-shot [ip]  copy a screenshot of the phone and save it
//
// Started with a server address it connects straight away; otherwise it
// shows a connect screen filled in with the last session's settings.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"runtime"
	"sync/atomic"
	"time"

	"github.com/aleynikovich/remoter/internal/update"
	"github.com/aleynikovich/remoter/internal/viewer"
)

// assetName is the release file holding this program.
func assetName() string {
	name := "remoter-viewer-" + runtime.GOOS + "-" + runtime.GOARCH
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return name
}

func init() {
	// SDL's window and event calls must all come from the main thread.
	runtime.LockOSThread()
}

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "phone":
			phoneMain(os.Args[2:])
			return
		case "phone-shot":
			phoneShotMain(os.Args[2:])
			return
		}
	}
	password := flag.String("password", "", "session password (default: the last one used)")
	id := flag.String("id", "", "computer ID, if the server has more than one")
	fullscreen := flag.Bool("fullscreen", false, "start full screen")
	tiles := flag.Bool("jpeg", false, "ask for JPEG tiles instead of H.264 video")
	noUpdate := flag.Bool("no-update", false, "do not update automatically")
	showVersion := flag.Bool("version", false, "print the version and exit")
	check := flag.Bool("selftest", false, "check that the window and video libraries work here, then exit")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "usage: remoter-viewer [flags] [server:port]\n")
		flag.PrintDefaults()
	}
	flag.Parse()
	if *showVersion {
		fmt.Println(version())
		return
	}
	if *check {
		if err := selfTest(); err != nil {
			fmt.Println(err)
			os.Exit(1)
		}
		fmt.Println("ok")
		return
	}
	setupLog()

	cfgPath, err := viewer.ConfigPath()
	if err != nil {
		log.Printf("settings will not be saved: %v", err)
	}
	cfg := viewer.LoadConfig(cfgPath)
	server := flag.Arg(0)
	if server != "" {
		cfg.Server, cfg.ID = server, *id
	}
	if *password != "" {
		cfg.Password = *password
	}

	exe, err := update.Executable()
	updated := new(atomic.Bool)
	if err == nil && update.Version != "" && !*noUpdate {
		go update.Cleanup(exe)
		go keepUpdated(exe, updated)
	}

	if err := loadSDL(); err != nil {
		fatal(err)
	}
	var newDec func() (videoDecoder, error)
	if !*tiles {
		if newDec, err = videoDecoders(); err != nil {
			log.Printf("H.264 off, using JPEG tiles: %v", err)
		}
	}
	a, err := newApp(cfg, cfgPath, *fullscreen, newDec, updated)
	if err != nil {
		fatal(err)
	}
	if server != "" {
		a.connect()
	}
	if a.run() {
		log.Printf("restarting into the new version")
		log.Fatalf("restart failed, start the viewer again: %v", update.Restart(exe, os.Environ()))
	}
}

func fatal(err error) {
	showError(err)
	log.Fatal(err)
}

func version() string {
	if update.Version == "" {
		return "dev"
	}
	return update.Version
}

// keepUpdated installs newer releases of the viewer as they are published.
// The window restarts into one the next time no session is open.
func keepUpdated(exe string, updated *atomic.Bool) {
	client := &http.Client{Timeout: 5 * time.Minute}
	c := &update.Checker{Current: update.Version, Asset: assetName(), Client: client}
	for {
		rel, asset, err := c.Latest(context.Background())
		switch {
		case err != nil:
			log.Printf("update check: %v", err)
		case rel != nil:
			log.Printf("installing %s", rel.Tag)
			if err := update.Install(context.Background(), client, exe, asset, rel.Tag); err != nil {
				log.Printf("update to %s: %v", rel.Tag, err)
			} else {
				updated.Store(true)
				return
			}
		}
		time.Sleep(10 * time.Minute)
	}
}

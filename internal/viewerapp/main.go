//go:build linux || windows

// Package viewerapp is Remoter's desktop viewer for Linux and Windows: a
// native window that shows a Remoter host's screen and hands it the
// keyboard and mouse, desktop shortcuts included. The remoter program runs
// it as
//
//	remoter view [-password root] [-id 123456789] [-fullscreen] [server:port]
//	remoter phone [ip]       mirror and control an Android phone
//	remoter phone-shot [ip]  copy a screenshot of the phone and save it
//
// Started with a server address it connects straight away; otherwise it
// shows a connect screen filled in with the last session's settings.
package viewerapp

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"sync/atomic"
	"time"

	"github.com/aleynikovich/remoter/internal/update"
	"github.com/aleynikovich/remoter/internal/viewer"
)

func init() {
	// SDL's window and event calls must all come from the main thread.
	runtime.LockOSThread()
}

// Main runs the viewer with the arguments after "remoter view" (or after
// the program name, for copies still installed as remoter-viewer). It must
// be called from the main goroutine.
func Main(args []string) {
	if len(args) > 0 {
		switch args[0] {
		case "phone":
			phoneMain(args[1:])
			return
		case "phone-shot":
			phoneShotMain(args[1:])
			return
		}
	}
	flags := flag.NewFlagSet("remoter view", flag.ExitOnError)
	password := flags.String("password", "", "session password (default: the last one used)")
	id := flags.String("id", "", "computer ID, if the server has more than one")
	fullscreen := flags.Bool("fullscreen", false, "start full screen")
	tiles := flags.Bool("jpeg", false, "ask for JPEG tiles instead of H.264 video")
	noUpdate := flags.Bool("no-update", false, "do not update automatically")
	showVersion := flags.Bool("version", false, "print the version and exit")
	check := flags.Bool("selftest", false, "check that the window and video libraries work here, then exit")
	flags.Usage = func() {
		fmt.Fprintf(flags.Output(), "usage: remoter view [flags] [server:port]\n")
		flags.PrintDefaults()
	}
	flags.Parse(args)
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
	// Opened from the desktop, a console window may have come with it.
	hideConsole()
	setupLog()

	cfgPath, err := viewer.ConfigPath()
	if err != nil {
		log.Printf("settings will not be saved: %v", err)
	}
	cfg := viewer.LoadConfig(cfgPath)
	server := flags.Arg(0)
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
		log.Fatalf("restart failed, start the viewer again: %v", restart(exe))
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

// restart starts the new version with the same arguments and exits. On
// Windows it gets no console window, which the viewer does not need.
func restart(exe string) error {
	if runtime.GOOS != "windows" {
		return update.Restart(exe, os.Environ())
	}
	cmd := exec.Command(exe, os.Args[1:]...)
	update.HideWindow(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	os.Exit(0)
	return nil
}

// keepUpdated installs newer releases of the app as they are published.
// The window restarts into one the next time no session is open.
func keepUpdated(exe string, updated *atomic.Bool) {
	client := &http.Client{Timeout: 5 * time.Minute}
	c := update.AppChecker(client)
	for {
		rel, asset, err := c.Latest(context.Background())
		switch {
		case err != nil:
			log.Printf("update check: %v", err)
			setUpdateState("update check failed: " + err.Error())
		case rel != nil && update.InstalledVersion(context.Background(), exe) == rel.Tag:
			// A host running this same file installed it already.
			setUpdateState(rel.Tag + " installed, restarting when you disconnect")
			updated.Store(true)
			return
		case rel != nil && !update.Replaceable(exe):
			// Installed in Program Files: the boot service updates it.
			setUpdateState(rel.Tag + " is out, the Remoter service is installing it")
		case rel != nil:
			log.Printf("installing %s", rel.Tag)
			setUpdateState("installing " + rel.Tag + "…")
			if err := update.Install(context.Background(), client, exe, asset, rel.Tag); err != nil {
				log.Printf("update to %s: %v", rel.Tag, err)
				setUpdateState("update to " + rel.Tag + " failed: " + err.Error())
			} else {
				setUpdateState(rel.Tag + " installed, restarting when you disconnect")
				updated.Store(true)
				return
			}
		default:
			setUpdateState("up to date")
		}
		time.Sleep(2 * time.Minute)
	}
}

// updateState says what the updater last did, for the start screen.
var updateState atomic.Pointer[string]

func setUpdateState(s string) { updateState.Store(&s) }

// versionLine is the build and update state shown on the start screen.
func versionLine() string {
	v := version()
	if update.Version == "" {
		return v + " (local build, does not update)"
	}
	if s := updateState.Load(); s != nil {
		return v + " · " + *s
	}
	return v
}

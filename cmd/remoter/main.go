// Command remoter shares this computer's screen. By default it runs the relay
// (with the web viewer) and the host together, so one program on the shared
// computer is all that is needed; -mode picks just one of them. It keeps
// itself up to date from the project's GitHub releases.
package main

import (
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"time"

	"github.com/aleynikovich/remoter/internal/capture"
	"github.com/aleynikovich/remoter/internal/diag"
	"github.com/aleynikovich/remoter/internal/h264"
	"github.com/aleynikovich/remoter/internal/relay"
	"github.com/aleynikovich/remoter/internal/update"
	"github.com/aleynikovich/remoter/web"
)

// idEnv carries the host's session ID across a restart, so open viewers
// reconnect to the same ID.
const idEnv = "REMOTER_HOST_ID"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "send" {
		os.Exit(runSend(os.Args[2:]))
	}
	mode := flag.String("mode", "all", `what to run: "all" (relay and host), "server" (relay only), "host" (host only) or "share" (only receive files and links from your other devices)`)
	addr := flag.String("addr", ":8080", "relay listen address")
	server := flag.String("server", "", "relay host endpoint for -mode host, e.g. ws://relay:8080/ws/host")
	var ho hostOptions
	flag.StringVar(&ho.password, "password", "root", "session password (random if set to \"\")")
	flag.IntVar(&ho.fps, "fps", 0, "maximum frames per second, 0 for no limit")
	flag.IntVar(&ho.quality, "quality", 60, "JPEG quality 1-100 for changing areas (still areas are resent sharper)")
	flag.IntVar(&ho.display, "display", 0, "display index to share")
	flag.BoolVar(&ho.test, "test-pattern", false, "stream a generated test pattern instead of the screen")
	flag.BoolVar(&ho.viewOnly, "view-only", false, "let viewers watch but not control this computer")
	flag.BoolVar(&ho.noClipboard, "no-clipboard", false, "do not share the clipboard with viewers")
	flag.BoolVar(&ho.noVideo, "no-video", false, "send JPEG images instead of H.264 video")
	flag.IntVar(&ho.kbps, "bitrate", 8000, "H.264 target bit rate in kbit/s")
	noUpdate := flag.Bool("no-update", false, "do not update automatically")
	checkEvery := flag.Duration("update-interval", 2*time.Minute, "how often to look for a new version")
	showVersion := flag.Bool("version", false, "print the version and exit")
	console := flag.Bool("console", false, "Windows: run in this window instead of in the background")
	background := flag.Bool("background", false, "run without a window (what starting Remoter normally does on Windows)")
	stopFlag := flag.Bool("stop", false, "stop the Remoter running on this computer")
	noAutostart := flag.Bool("no-autostart", false, "Windows: do not start Remoter when you log in")
	selfTest := flag.Bool("h264-selftest", false, "check that the H.264 codec works, then exit (used internally)")
	noElevate := flag.Bool("no-elevate", false, "Windows: do not restart with administrator rights even where that needs no prompt")
	captureTest := flag.Bool("capture-selftest", false, "check that fast screen capture works, then exit (used internally)")
	flag.Parse()

	if *captureTest {
		if err := capture.SelfTestDXGI(); err != nil {
			log.Fatal(err)
		}
		return
	}
	if *selfTest {
		if err := h264.SelfTest(); err != nil {
			log.Fatal(err)
		}
		return
	}

	if *showVersion {
		fmt.Println(version())
		return
	}
	runServer, runAgent := *mode == "all" || *mode == "server", *mode == "all" || *mode == "host"
	shareOnly := *mode == "share"
	if !runServer && !runAgent && !shareOnly {
		log.Fatalf("unknown -mode %q", *mode)
	}
	if runAgent {
		ho.server = *server
		if ho.server == "" {
			if !runServer {
				log.Fatal("-mode host needs -server")
			}
			ho.server = "ws://" + localAddr(*addr) + "/ws/host"
		}
	}
	if *stopFlag {
		err := stopRunning(*addr)
		if err == nil && backgroundSupported && *noAutostart {
			if exe, xerr := update.Executable(); xerr == nil {
				err = setAutostart(false, exe, nil)
			}
		}
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}
		fmt.Println("Remoter stopped.")
		return
	}
	// The flags the user chose, kept for restarts and for starting at login.
	userArgs := withoutFlags(os.Args[1:], "background", "console", "no-elevate")
	if backgroundSupported && !*console && !*background {
		if launch(*addr, runServer, ho.password, userArgs) {
			return
		}
		fmt.Println("Running in this window instead.")
		*console = true
	}
	// Without administrator rights Windows silently drops input sent to
	// admin windows. Where that takes no prompt, restart with them.
	if *background && !*noElevate && runAgent && !isAdmin() && canElevateSilently() {
		if exe, err := os.Executable(); err == nil {
			if err := startElevated(exe, append(os.Args[1:], "-no-elevate")); err == nil {
				return
			}
		}
	}
	var logTo io.Writer = os.Stderr
	if *background {
		if f, err := openLog(); err == nil {
			logTo = f
		}
	}
	// Viewers can ask for the recent log, so nobody has to dig for the file.
	log.SetOutput(io.MultiWriter(logTo, diag.Log))
	log.Printf("Remoter %s", version())
	if runtime.GOOS == "windows" && !isAdmin() {
		log.Print("running without administrator rights: Windows blocks input to programs running as administrator")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, restart := context.WithCancelCause(ctx)

	exe, err := update.Executable()
	if err != nil {
		log.Printf("updates disabled: %v", err)
	} else if update.Version == "" {
		log.Print("updates disabled: this is a local build")
	} else if !*noUpdate {
		go update.Cleanup(exe)
		go keepUpdated(ctx, exe, *checkEvery, restart)
	}
	if *background && exe != "" && update.Version != "" {
		if err := setAutostart(!*noAutostart, exe, userArgs); err != nil {
			log.Printf("could not set up starting at login: %v", err)
		}
	}

	done := make(chan struct{})
	var srv *http.Server
	if runServer || shareOnly {
		srv, err = startServer(ctx, *addr, runServer, func() { restart(errStop) })
		if ctx.Err() != nil && !errors.Is(context.Cause(ctx), errRestart) {
			return
		}
		if err != nil && ctx.Err() == nil {
			log.Fatal(err)
		}
	}
	var agentID func() string
	if runAgent {
		ho.id = os.Getenv(idEnv)
		a := newAgent(ho)
		agentID = func() string { return a.ID }
		go func() {
			runHost(ctx, a)
			close(done)
		}()
	} else {
		close(done)
	}

	<-ctx.Done()
	if srv != nil {
		srv.Close()
	}
	<-done
	if !errors.Is(context.Cause(ctx), errRestart) {
		return
	}
	env := os.Environ()
	if agentID != nil && agentID() != "" {
		env = append(env, idEnv+"="+agentID())
	}
	log.Print("restarting into the new version")
	if backgroundSupported && !*console {
		err = startDetached(exe, append(userArgs, "-background"), env)
		if err == nil {
			os.Exit(0)
		}
	} else {
		err = update.Restart(exe, env)
	}
	log.Fatalf("restart failed, start Remoter again by hand: %v", err)
}

var (
	errRestart = errors.New("restart for update")
	errStop    = errors.New("stopped by another Remoter")
)

func version() string {
	if update.Version == "" {
		return "dev (local build)"
	}
	return update.Version
}

// startServer serves the relay and web viewer, and file sharing with your
// other devices. With relay false it serves only file sharing (-mode share).
func startServer(ctx context.Context, addr string, relayToo bool, stop func()) (*http.Server, error) {
	static, err := fs.Sub(web.Files, "static")
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.Handle("/shutdown", shutdownHandler(stop))
	mux.HandleFunc("/ping", pingHandler)
	mux.HandleFunc("/speedtest", speedtestHandler)
	// Viewers that cannot ask Tailscale themselves list the tailnet here.
	_, port, _ := net.SplitHostPort(addr)
	mux.Handle("/devices", devicesHandler(cmp.Or(port, "8080")))
	registerShare(mux)
	if relayToo {
		mux.Handle("/ws/", relay.New().Handler())
		// The viewer shows which build is running and notices when it
		// changes. A share-only Remoter leaves it out, so viewers do not
		// list it as a computer to control.
		mux.HandleFunc("/version", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			fmt.Fprint(w, version())
		})
		// Embedded files carry no dates, so tell browsers to check every
		// time; otherwise a viewer could keep running an old viewer.js
		// after an update.
		files := http.FileServerFS(static)
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-cache")
			files.ServeHTTP(w, r)
		})
	}

	// If the port is taken, by the previous process right after an update or
	// by an older Remoter still running, wait for it. That lets a new copy be
	// started first and take over the moment the old one is closed, without
	// ever leaving the computer unreachable.
	var ln net.Listener
	start := time.Now()
	waiting := false
	for {
		ln, err = net.Listen("tcp", addr)
		if err == nil {
			break
		}
		if !waiting && time.Since(start) > 3*time.Second {
			log.Printf("port %s is in use (%v); waiting for it, close the other Remoter or server to take over", portOf(addr), err)
			waiting = true
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	if waiting {
		log.Print("took over the port")
	}
	srv := &http.Server{Handler: mux}
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	}()
	if relayToo {
		log.Printf("viewer at http://localhost%s (or this computer's address)", portOf(addr))
	} else {
		log.Printf("receiving files and links on %s; drop files to send at http://localhost%s/share/", portOf(addr), portOf(addr))
	}
	return srv, nil
}

// localAddr turns a listen address like ":8080" into one to dial.
func localAddr(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}

func portOf(addr string) string {
	if _, port, err := net.SplitHostPort(addr); err == nil {
		return ":" + port
	}
	return ""
}

// keepUpdated checks for a new release now and every interval, installs it,
// and asks main to restart.
func keepUpdated(ctx context.Context, exe string, every time.Duration, restart context.CancelCauseFunc) {
	client := &http.Client{Timeout: 5 * time.Minute}
	c := &update.Checker{Current: update.Version, Asset: update.AssetName(), Client: client}
	c.API = os.Getenv("REMOTER_UPDATE_API") // for trying updates against a test server
	// A release that did not install is not downloaded again.
	failed := ""
	saidCurrent := false
	for {
		rel, asset, err := c.Latest(ctx)
		switch {
		case err != nil:
			if ctx.Err() == nil {
				log.Printf("update check failed: %v", err)
			}
		case rel == nil && !saidCurrent:
			// Said once, so the window shows that checking works.
			log.Printf("%s is the newest build, checking for updates every %v", update.Version, every)
			saidCurrent = true
		case rel != nil && rel.Tag != failed:
			log.Printf("downloading %s", rel.Tag)
			if err := update.Install(ctx, client, exe, asset, rel.Tag); err != nil {
				log.Printf("update to %s failed: %v", rel.Tag, err)
				failed = rel.Tag
				break
			}
			log.Printf("updated to %s", rel.Tag)
			restart(errRestart)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(every):
		}
	}
}

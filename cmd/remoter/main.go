// Command remoter shares this computer's screen. By default it runs the relay
// (with the web viewer) and the host together, so one program on the shared
// computer is all that is needed; -mode picks just one of them. It keeps
// itself up to date from the project's GitHub releases.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"time"

	"github.com/aleynikovich/remoter/internal/relay"
	"github.com/aleynikovich/remoter/internal/update"
	"github.com/aleynikovich/remoter/web"
)

// idEnv carries the host's session ID across a restart, so open viewers
// reconnect to the same ID.
const idEnv = "REMOTER_HOST_ID"

func main() {
	mode := flag.String("mode", "all", `what to run: "all" (relay and host), "server" (relay only) or "host" (host only)`)
	addr := flag.String("addr", ":8080", "relay listen address")
	server := flag.String("server", "", "relay host endpoint for -mode host, e.g. ws://relay:8080/ws/host")
	var ho hostOptions
	flag.StringVar(&ho.password, "password", "root", "session password (random if set to \"\")")
	flag.IntVar(&ho.fps, "fps", 30, "maximum frames per second")
	flag.IntVar(&ho.quality, "quality", 60, "JPEG quality 1-100 for changing areas (still areas are resent sharper)")
	flag.IntVar(&ho.display, "display", 0, "display index to share")
	flag.BoolVar(&ho.test, "test-pattern", false, "stream a generated test pattern instead of the screen")
	flag.BoolVar(&ho.viewOnly, "view-only", false, "let viewers watch but not control this computer")
	flag.BoolVar(&ho.noClipboard, "no-clipboard", false, "do not share the clipboard with viewers")
	noUpdate := flag.Bool("no-update", false, "do not update automatically")
	checkEvery := flag.Duration("update-interval", 2*time.Minute, "how often to look for a new version")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(version())
		return
	}
	runServer, runAgent := *mode != "host", *mode != "server"
	if *mode != "all" && *mode != "server" && *mode != "host" {
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
	log.Printf("Remoter %s", version())

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

	done := make(chan struct{})
	var srv *http.Server
	if runServer {
		srv, err = startServer(*addr)
		if err != nil {
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
	log.Fatalf("restart failed, start Remoter again by hand: %v", update.Restart(exe, env))
}

var errRestart = errors.New("restart for update")

func version() string {
	if update.Version == "" {
		return "dev (local build)"
	}
	return update.Version
}

// startServer serves the relay and web viewer. Right after an update the
// previous process may still be letting go of the port, so it retries.
func startServer(addr string) (*http.Server, error) {
	static, err := fs.Sub(web.Files, "static")
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.Handle("/ws/", relay.New().Handler())
	mux.Handle("/", http.FileServerFS(static))

	var ln net.Listener
	for try := 0; ; try++ {
		ln, err = net.Listen("tcp", addr)
		if err == nil || try == 20 {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if err != nil {
		return nil, err
	}
	srv := &http.Server{Handler: mux}
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	}()
	log.Printf("viewer at http://localhost%s (or this computer's address)", portOf(addr))
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
	failed := "" // a release that did not install is not downloaded again
	for {
		rel, asset, err := c.Latest(ctx)
		switch {
		case err != nil:
			if ctx.Err() == nil {
				log.Printf("update check failed: %v", err)
			}
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

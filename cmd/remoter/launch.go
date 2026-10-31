package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aleynikovich/remoter/internal/update"
)

// On Windows, starting Remoter from its window only launches it: the real
// work happens in a background copy with no window, so closing the window
// (or logging the console out) never drops the connection. The background
// copy also registers itself to start at login.

// stopHeader must be on a /shutdown request. Browsers cannot send it to
// another site without permission, so a web page cannot stop Remoter.
const stopHeader = "X-Remoter-Stop"

// launch starts the background copy, replacing one that runs another build,
// and tells the user what happened. It reports false if no background copy
// could be started, and the caller then runs in this window instead, so the
// computer stays reachable.
func launch(addr string, server bool, password string, args []string) bool {
	exe, err := update.Executable()
	if err != nil {
		fmt.Println("Could not start Remoter in the background:", err)
		return false
	}
	if server {
		if v, ok := runningVersion(addr); ok {
			if v == version() {
				fmt.Printf("Remoter %s is already running in the background.\n", v)
				printInfo(addr, password)
				pause()
				return true
			}
			fmt.Printf("Replacing the running Remoter %s with %s.\n", v, version())
			if err := stopRunning(addr); err != nil {
				fmt.Println("Could not stop it:", err)
			}
		}
	}
	if err := startDetached(exe, append(args, "-background"), os.Environ()); err != nil {
		fmt.Println("Could not start Remoter in the background:", err)
		return false
	}
	defer pause()
	if !server {
		fmt.Printf("Remoter %s is running in the background.\n", version())
		printInfo(addr, password)
		return true
	}
	// Wait until it serves. If an older Remoter without this launcher still
	// holds the port, the background copy takes over once that is closed.
	start := time.Now()
	told := false
	for {
		if v, ok := runningVersion(addr); ok && v == version() {
			break
		}
		if !told && time.Since(start) > 5*time.Second {
			fmt.Printf("Another program (probably an older Remoter window) is using port %s.\n", portOf(addr))
			fmt.Println("Close that window: this Remoter takes over the moment it is gone.")
			told = true
		}
		time.Sleep(500 * time.Millisecond)
	}
	fmt.Printf("Remoter %s is running in the background.\n", version())
	printInfo(addr, password)
	return true
}

func printInfo(addr, password string) {
	fmt.Println()
	fmt.Printf("  Viewer:    http://<this computer's address>%s\n", portOf(addr))
	if password == "" {
		fmt.Println("  Password:  random, see the log")
	} else {
		fmt.Printf("  Password:  %s\n", password)
	}
	fmt.Printf("  Log:       %s\n", logPath())
	fmt.Println()
	fmt.Println("It starts when you log in and updates itself. You can close this window.")
	fmt.Println("To stop it, run this program with -stop (add -no-autostart to also stop starting it at login).")
}

// pause keeps a double-clicked window open long enough to read, unless the
// viewer window opens next.
func pause() {
	if openViewer {
		return
	}
	fmt.Println()
	fmt.Print("Press Enter to close this window.")
	done := make(chan struct{})
	go func() {
		bufio.NewReader(os.Stdin).ReadString('\n')
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Minute):
	}
}

// runningVersion asks a Remoter on addr which build it is.
func runningVersion(addr string) (string, bool) {
	client := http.Client{Timeout: time.Second}
	resp, err := client.Get("http://" + localAddr(addr) + "/version")
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 100))
	if resp.StatusCode != http.StatusOK {
		return "", false
	}
	return strings.TrimSpace(string(b)), true
}

// stopRunning asks the Remoter on addr to exit and waits until it has.
func stopRunning(addr string) error {
	req, _ := http.NewRequest(http.MethodPost, "http://"+localAddr(addr)+"/shutdown", nil)
	req.Header.Set(stopHeader, "1")
	client := http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("no Remoter is running on port %s", portOf(addr))
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Remoter on port %s refused to stop: %s", portOf(addr), resp.Status)
	}
	for range 50 {
		if _, ok := runningVersion(addr); !ok {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return errors.New("Remoter did not stop")
}

// shutdownHandler lets a Remoter started on this computer stop this one.
func shutdownHandler(stop func()) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get(stopHeader) == "" || !fromLoopback(r) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		log.Print("asked to stop by another Remoter on this computer")
		w.WriteHeader(http.StatusOK)
		go func() {
			time.Sleep(100 * time.Millisecond) // let the answer go out
			stop()
		}()
	}
}

func fromLoopback(r *http.Request) bool {
	host := r.RemoteAddr
	if i := strings.LastIndex(host, ":"); i >= 0 {
		host = host[:i]
	}
	host = strings.Trim(host, "[]")
	return host == "127.0.0.1" || host == "::1"
}

// openLog sends the log of the background copy to a file, keeping one
// previous file once it grows large.
func openLog() (*os.File, error) {
	path := logPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	if fi, err := os.Stat(path); err == nil && fi.Size() > 5<<20 {
		os.Rename(path, path+".1")
	}
	return os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
}

// withoutFlags drops the named boolean flags from args.
func withoutFlags(args []string, names ...string) []string {
	var out []string
	for _, a := range args {
		name := strings.TrimLeft(a, "-")
		if i := strings.IndexByte(name, '='); i >= 0 {
			name = name[:i]
		}
		drop := false
		for _, n := range names {
			drop = drop || (strings.HasPrefix(a, "-") && name == n)
		}
		if !drop {
			out = append(out, a)
		}
	}
	return out
}

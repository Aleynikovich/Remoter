package main

import (
	"bufio"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aleynikovich/remoter/internal/lan"
	"github.com/aleynikovich/remoter/internal/share"
)

//go:embed sharepage.html
var sharePage []byte

// target is a device that receives shares.
type target struct {
	Name string `json:"name"`
	OS   string `json:"os"`
	IP   string `json:"ip"`
	base string
}

func deviceName() string {
	if n, err := os.Hostname(); err == nil && n != "" {
		n, _, _ = strings.Cut(n, ".")
		return n
	}
	return runtime.GOOS
}

// registerShare lets your other devices send files and links here, and gives
// this computer a page to drop files on to send them (/share/).
func registerShare(mux *http.ServeMux) {
	desk := share.Desktop{}
	srv := &share.Server{
		Name:    deviceName(),
		Version: version(),
		Dir:     share.DownloadDir(),
		Allow:   fromHome,
		Files:   desk.Files,
		Text:    desk.Text,
	}
	srv.Register(mux)
	log.Printf("share: files from your other devices are saved to %s", srv.Dir)

	// The drop page and what it calls only work from this computer: it
	// sends as this computer, so other devices must not use it as a relay.
	local := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if !fromLoopback(r) {
				http.Error(w, "only from this computer", http.StatusForbidden)
				return
			}
			w.Header().Set("Cache-Control", "no-store")
			h(w, r)
		}
	}
	mux.HandleFunc("GET /share/{$}", local(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(sharePage)
	}))
	mux.HandleFunc("GET /share/targets", local(func(w http.ResponseWriter, r *http.Request) {
		ts, err := findTargets(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string][]target{"targets": ts})
	}))
	// POST /share/send?to=IP&name=file&index=1&count=2 (body = file), or
	// ?to=IP&text=1 (body = text). The custom header keeps other web
	// pages from using it.
	mux.HandleFunc("POST /share/send", local(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Remoter-Send") == "" {
			http.Error(w, "missing X-Remoter-Send header", http.StatusBadRequest)
			return
		}
		q := r.URL.Query()
		to := q.Get("to")
		if net.ParseIP(to) == nil {
			http.Error(w, "to must be an IP address", http.StatusBadRequest)
			return
		}
		base := "http://" + net.JoinHostPort(to, share.Port)
		c := &share.Client{From: deviceName()}
		var err error
		if q.Get("text") != "" {
			var b []byte
			b, err = io.ReadAll(io.LimitReader(r.Body, share.MaxText))
			if err == nil {
				err = c.SendText(r.Context(), base, string(b))
			}
		} else {
			index, _ := strconv.Atoi(q.Get("index"))
			count, _ := strconv.Atoi(q.Get("count"))
			err = c.Send(r.Context(), base, q.Get("name"), r.Body, r.ContentLength, index, count)
		}
		if err != nil {
			log.Printf("share: sending to %s failed: %v", to, err)
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
}

// findTargets lists the devices on the home network and the tailnet that
// receive shares, phones first. A device on both is reached over the home
// network.
func findTargets(ctx context.Context) ([]target, error) {
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	home := make(chan []lan.Answer, 1)
	go func() {
		as, _ := lan.Discover(ctx, 1500*time.Millisecond)
		home <- as
	}()
	devices, err := tailnetDevices(ctx)
	ts := []target{}
	self := lan.TailscaleIP()
	onLAN := map[string]bool{} // tailnet IPs answered for on the home network
	for _, a := range <-home {
		if !a.Share || a.Tailnet != "" && a.Tailnet == self || slices.Contains(lan.Addresses(), a.IP) {
			continue
		}
		ts = append(ts, target{Name: a.Name, OS: a.OS, IP: a.IP, base: "http://" + a.Addr()})
		if a.Tailnet != "" {
			onLAN[a.Tailnet] = true
		}
	}
	if err != nil && len(ts) == 0 {
		return nil, err
	}
	c := &share.Client{HTTP: &http.Client{Timeout: 2 * time.Second}}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, d := range devices {
		if d.Self || !d.Online || onLAN[d.IP] {
			continue
		}
		wg.Add(1)
		go func(d device) {
			defer wg.Done()
			base := "http://" + net.JoinHostPort(d.IP, share.Port)
			info, err := c.Info(ctx, base)
			if err != nil {
				return
			}
			t := target{Name: cmpOr(info.Name, d.Name), OS: cmpOr(info.OS, d.OS), IP: d.IP, base: base}
			mu.Lock()
			ts = append(ts, t)
			mu.Unlock()
		}(d)
	}
	wg.Wait()
	sort.Slice(ts, func(i, j int) bool {
		if pi, pj := isPhone(ts[i]), isPhone(ts[j]); pi != pj {
			return pi
		}
		return strings.ToLower(ts[i].Name) < strings.ToLower(ts[j].Name)
	})
	return ts, nil
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func isPhone(t target) bool {
	return strings.EqualFold(t.OS, "android") || strings.EqualFold(t.OS, "ios")
}

// runSend is "remoter send": send files, text or the clipboard to another
// device. Run from a key binding, it asks with pickers and answers with
// notifications instead of the terminal.
func runSend(args []string) int {
	fs := flag.NewFlagSet("send", flag.ContinueOnError)
	to := fs.String("to", os.Getenv("REMOTER_SEND_TO"), "device to send to: a name (or part of one) or a Tailscale IP; asks when there is a choice")
	text := fs.String("text", "", `send this text or link instead of files ("-" reads it from standard input)`)
	clip := fs.Bool("clipboard", false, "send the clipboard text")
	list := fs.Bool("list", false, "list the devices you can send to")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Usage: remoter send [-to device] [file ...]\n       remoter send [-to device] -text 'text or link' | -clipboard\n\nWith no files, a file chooser opens.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	tty := isTerminal()
	fail := func(err error) int {
		fmt.Fprintln(os.Stderr, "remoter send:", err)
		if !tty {
			share.Notify(share.Notice{Title: "Sending failed", Body: err.Error()})
		}
		return 1
	}
	ctx := context.Background()

	if *list {
		ts, err := findTargets(ctx)
		if err != nil {
			return fail(err)
		}
		for _, t := range ts {
			fmt.Printf("%-24s %-8s %s\n", t.Name, t.OS, t.IP)
		}
		return 0
	}

	files := fs.Args()
	var message string
	switch {
	case *clip:
		s, err := share.ReadClipboard()
		if err != nil {
			return fail(err)
		}
		message = s
	case *text == "-":
		b, err := io.ReadAll(io.LimitReader(os.Stdin, share.MaxText))
		if err != nil {
			return fail(err)
		}
		message = string(b)
	case *text != "":
		message = *text
	case len(files) == 0:
		files = chooseFiles()
		if len(files) == 0 {
			return 0
		}
	}
	if (*clip || *text != "") && strings.TrimSpace(message) == "" {
		return fail(errors.New("nothing to send: the text is empty"))
	}

	t, err := chooseTarget(ctx, *to, tty)
	if err != nil {
		return fail(err)
	}
	if t == nil {
		return 0 // picker closed
	}
	c := &share.Client{From: deviceName()}
	if message != "" {
		if err := c.SendText(ctx, t.base, message); err != nil {
			return fail(fmt.Errorf("to %s: %w", t.Name, err))
		}
		what := "text"
		if share.IsLink(strings.TrimSpace(message)) {
			what = "link"
		}
		done(tty, fmt.Sprintf("Sent %s to %s", what, t.Name))
		return 0
	}
	for i, f := range files {
		var progress func(sent, total int64)
		if tty {
			name := f
			progress = func(sent, total int64) {
				if total > 0 {
					fmt.Fprintf(os.Stderr, "\r%s: %d%%", name, sent*100/total)
				}
			}
		}
		err := c.SendFile(ctx, t.base, f, i+1, len(files), progress)
		if tty {
			fmt.Fprintln(os.Stderr)
		}
		if err != nil {
			return fail(fmt.Errorf("sending %s to %s: %w", f, t.Name, err))
		}
	}
	if len(files) == 1 {
		done(tty, fmt.Sprintf("Sent %s to %s", baseName(files[0]), t.Name))
	} else {
		done(tty, fmt.Sprintf("Sent %d files to %s", len(files), t.Name))
	}
	return 0
}

func done(tty bool, msg string) {
	if tty {
		fmt.Println(msg)
	} else {
		share.Notify(share.Notice{Title: msg})
	}
}

func baseName(p string) string {
	p = strings.ReplaceAll(p, "\\", "/")
	return p[strings.LastIndex(p, "/")+1:]
}

// chooseTarget finds the device to send to: the one named, the only one,
// or the one the user picks (phones first, so Enter sends to the phone).
func chooseTarget(ctx context.Context, want string, tty bool) (*target, error) {
	if ip := net.ParseIP(want); ip != nil {
		return &target{Name: want, IP: want, base: "http://" + net.JoinHostPort(want, share.Port)}, nil
	}
	ts, err := findTargets(ctx)
	if err != nil {
		return nil, err
	}
	if want != "" {
		var match []target
		for _, t := range ts {
			if strings.EqualFold(t.Name, want) {
				return &t, nil
			}
			if strings.Contains(strings.ToLower(t.Name), strings.ToLower(want)) {
				match = append(match, t)
			}
		}
		if len(match) == 0 {
			return nil, fmt.Errorf("no device called %q is receiving; is Remoter running there?", want)
		}
		ts = match
	}
	switch len(ts) {
	case 0:
		return nil, errors.New("no device is receiving: open Remoter on the phone, or run remoter -mode share on the PC")
	case 1:
		return &ts[0], nil
	}
	labels := make([]string, len(ts))
	for i, t := range ts {
		labels[i] = fmt.Sprintf("%s (%s)", t.Name, t.OS)
	}
	i := pick(labels, tty)
	if i < 0 {
		return nil, nil
	}
	return &ts[i], nil
}

// pick asks which of labels to use: in the terminal, or with a launcher
// menu when run from a key binding. -1 means the user cancelled.
func pick(labels []string, tty bool) int {
	if tty {
		for i, l := range labels {
			fmt.Printf("%d) %s\n", i+1, l)
		}
		fmt.Print("Send to [1]: ")
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		line = strings.TrimSpace(line)
		if line == "" {
			return 0
		}
		n, err := strconv.Atoi(line)
		if err != nil || n < 1 || n > len(labels) {
			return -1
		}
		return n - 1
	}
	menus := [][]string{
		{"fuzzel", "--dmenu", "--prompt=Send to: "},
		{"wofi", "--dmenu", "--prompt=Send to"},
		{"rofi", "-dmenu", "-p", "Send to"},
		{"tofi", "--prompt-text=Send to: "},
		{"bemenu", "-p", "Send to"},
		{"zenity", "--list", "--title=Send to", "--column=Device", "--hide-header"},
	}
	for _, m := range menus {
		if _, err := exec.LookPath(m[0]); err != nil {
			continue
		}
		cmd := exec.Command(m[0], m[1:]...)
		if m[0] == "zenity" {
			cmd.Args = append(cmd.Args, labels...)
		} else {
			cmd.Stdin = strings.NewReader(strings.Join(labels, "\n") + "\n")
		}
		out, err := cmd.Output()
		if err != nil {
			return -1
		}
		choice := strings.TrimSpace(string(out))
		for i, l := range labels {
			if l == choice {
				return i
			}
		}
		return -1
	}
	return 0 // no menu program: the first, a phone if there is one
}

// chooseFiles opens a file chooser.
func chooseFiles() []string {
	var out []byte
	var err error
	switch {
	case have("zenity"):
		out, err = exec.Command("zenity", "--file-selection", "--multiple", "--separator=\n", "--title=Send files").Output()
	case have("kdialog"):
		out, err = exec.Command("kdialog", "--getopenfilename", "--multiple", "--separate-output", os.Getenv("HOME")).Output()
	default:
		fmt.Fprintln(os.Stderr, "remoter send: name the files to send (no zenity or kdialog for a file chooser)")
		return nil
	}
	if err != nil {
		return nil
	}
	var files []string
	for _, l := range strings.Split(string(out), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			files = append(files, l)
		}
	}
	return files
}

func have(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

func isTerminal() bool {
	st, err := os.Stdout.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/aleynikovich/remoter/internal/lan"
	"github.com/aleynikovich/remoter/internal/update"
)

// device is one computer on the Tailscale network, as /devices lists it for
// viewers that cannot ask Tailscale themselves (the Android app).
type device struct {
	Name    string `json:"name"`
	OS      string `json:"os"`
	IP      string `json:"ip"`
	Online  bool   `json:"online"`
	Self    bool   `json:"self"`
	Remoter string `json:"remoter,omitempty"` // build a Remoter there reports
}

const devicesCacheFor = 10 * time.Second

var errNoTailscale = errors.New("Tailscale is not installed")

// tailnetPrefixes are the addresses Tailscale hands out.
var tailnetPrefixes = []netip.Prefix{
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("fd7a:115c:a1e0::/48"),
}

// fromTailnet reports whether a request comes over Tailscale or from this
// computer. The device list is only for viewers already on the tailnet.
func fromTailnet(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	ip = ip.Unmap()
	if ip.IsLoopback() {
		return true
	}
	for _, p := range tailnetPrefixes {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// fromHome reports whether a request comes over Tailscale, from this
// computer or from the home network (a private address).
func fromHome(r *http.Request) bool {
	if fromTailnet(r) {
		return true
	}
	ap, err := netip.ParseAddrPort(r.RemoteAddr)
	return err == nil && lan.Local(ap.Addr())
}

// devicesHandler serves GET /devices: {"devices": [...]}, or 404 when
// Tailscale is not installed here.
func devicesHandler(port string) http.Handler {
	var mu sync.Mutex
	var cached []device
	var at time.Time
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !fromTailnet(r) {
			http.Error(w, "only for computers on your Tailscale network", http.StatusForbidden)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if cached == nil || time.Since(at) > devicesCacheFor {
			ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
			defer cancel()
			list, err := tailnetDevices(ctx)
			if errors.Is(err, errNoTailscale) {
				http.NotFound(w, r)
				return
			}
			if err != nil {
				http.Error(w, err.Error(), http.StatusServiceUnavailable)
				return
			}
			probe(ctx, list, port)
			cached, at = list, time.Now()
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		json.NewEncoder(w).Encode(map[string][]device{"devices": cached})
	})
}

func tailscaleCommand() (string, error) {
	if p, err := exec.LookPath("tailscale"); err == nil {
		return p, nil
	}
	if runtime.GOOS == "windows" {
		p := filepath.Join(os.Getenv("ProgramFiles"), "Tailscale", "tailscale.exe")
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", errNoTailscale
}

func tailnetDevices(ctx context.Context) ([]device, error) {
	cmd, err := tailscaleCommand()
	if err != nil {
		return nil, err
	}
	c := exec.CommandContext(ctx, cmd, "status", "--json")
	update.HideWindow(c)
	out, err := c.Output()
	if err != nil && len(out) == 0 {
		return nil, errors.New("tailscale status failed: " + err.Error())
	}
	return parseTailscaleStatus(out)
}

func parseTailscaleStatus(data []byte) ([]device, error) {
	type peer struct {
		HostName     string
		DNSName      string
		OS           string
		TailscaleIPs []string
		Online       bool
	}
	var st struct {
		BackendState string
		Self         *peer
		Peer         map[string]*peer
	}
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, errors.New("cannot read tailscale status: " + err.Error())
	}
	if st.BackendState != "" && st.BackendState != "Running" {
		return nil, errors.New("Tailscale is not connected (" + st.BackendState + ")")
	}
	list := []device{}
	add := func(p *peer, self bool) {
		if p == nil {
			return
		}
		d := device{Name: p.HostName, OS: p.OS, Online: p.Online || self, Self: self}
		if d.Name == "" {
			d.Name, _, _ = strings.Cut(p.DNSName, ".")
		}
		for _, ip := range p.TailscaleIPs {
			if !strings.Contains(ip, ":") {
				d.IP = ip
				break
			}
		}
		if d.IP != "" {
			list = append(list, d)
		}
	}
	add(st.Self, true)
	for _, p := range st.Peer {
		add(p, false)
	}
	// This computer first, then online ones, then by name.
	sort.SliceStable(list, func(i, j int) bool {
		a, b := list[i], list[j]
		if a.Self != b.Self {
			return a.Self
		}
		if a.Online != b.Online {
			return a.Online
		}
		return strings.ToLower(a.Name) < strings.ToLower(b.Name)
	})
	return list, nil
}

// probe asks each online device's /version which Remoter build it runs.
func probe(ctx context.Context, list []device, port string) {
	client := &http.Client{Timeout: 1500 * time.Millisecond}
	var wg sync.WaitGroup
	for i := range list {
		if !list[i].Online {
			continue
		}
		wg.Add(1)
		go func(d *device) {
			defer wg.Done()
			if d.Self {
				d.Remoter = version()
				return
			}
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+net.JoinHostPort(d.IP, port)+"/version", nil)
			if err != nil {
				return
			}
			resp, err := client.Do(req)
			if err != nil {
				return
			}
			defer resp.Body.Close()
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 100))
			if resp.StatusCode == http.StatusOK {
				d.Remoter = strings.TrimSpace(string(b))
			}
		}(&list[i])
	}
	wg.Wait()
}

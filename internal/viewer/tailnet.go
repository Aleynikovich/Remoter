package viewer

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"
)

// DefaultPort is where a Remoter host serves viewers unless told otherwise.
const DefaultPort = "8080"

// Device is a computer on the user's Tailscale network.
type Device struct {
	Name   string // host name
	OS     string // as Tailscale reports it: windows, linux, macOS, android...
	IP     string // its Tailscale IPv4 address
	Online bool
	Self   bool // the computer the viewer runs on
	// Remoter is the build a Remoter host on the device reports, or "" when
	// none answered on DefaultPort.
	Remoter string
}

// Addr is the server address to connect to the device.
func (d Device) Addr() string { return net.JoinHostPort(d.IP, DefaultPort) }

// tailscaleStatus is the part of `tailscale status --json` the viewer reads.
type tailscaleStatus struct {
	BackendState string
	Self         *tailscalePeer
	Peer         map[string]*tailscalePeer
}

type tailscalePeer struct {
	HostName     string
	DNSName      string
	OS           string
	TailscaleIPs []string
	Online       bool
}

// ErrNoTailscale means the tailscale command is not installed here.
var ErrNoTailscale = errors.New("Tailscale is not installed on this computer")

// tailscaleCommand finds the tailscale CLI.
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
	return "", ErrNoTailscale
}

// TailnetDevices lists the devices on the Tailscale network this computer
// is logged in to, without probing them.
func TailnetDevices(ctx context.Context) ([]Device, error) {
	cmd, err := tailscaleCommand()
	if err != nil {
		return nil, err
	}
	c := exec.CommandContext(ctx, cmd, "status", "--json")
	hideWindow(c)
	out, err := c.Output()
	if err != nil && len(out) == 0 {
		return nil, errors.New("tailscale status failed: " + err.Error())
	}
	return parseTailscaleStatus(out)
}

func parseTailscaleStatus(data []byte) ([]Device, error) {
	var st tailscaleStatus
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, errors.New("cannot read tailscale status: " + err.Error())
	}
	if st.BackendState != "" && st.BackendState != "Running" {
		return nil, errors.New("Tailscale is not connected (" + st.BackendState + ")")
	}
	var devices []Device
	add := func(p *tailscalePeer, self bool) {
		if p == nil {
			return
		}
		d := Device{Name: p.HostName, OS: p.OS, Online: p.Online || self, Self: self}
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
			devices = append(devices, d)
		}
	}
	add(st.Self, true)
	for _, p := range st.Peer {
		add(p, false)
	}
	SortDevices(devices)
	return devices, nil
}

// SortDevices puts computers with Remoter running first, then the others
// that are online, then the offline ones, each group by name.
func SortDevices(ds []Device) {
	rank := func(d Device) int {
		switch {
		case d.Remoter != "":
			return 0
		case d.Online:
			return 1
		}
		return 2
	}
	slices.SortStableFunc(ds, func(a, b Device) int {
		return cmp.Or(cmp.Compare(rank(a), rank(b)), cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)))
	})
}

// ProbeDevices asks every online device whether a Remoter host runs on it,
// filling in Remoter, and returns them sorted.
func ProbeDevices(ctx context.Context, ds []Device) []Device {
	client := &http.Client{Timeout: 1500 * time.Millisecond}
	var wg sync.WaitGroup
	for i := range ds {
		if !ds[i].Online {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			ds[i].Remoter = probe(ctx, client, ds[i].Addr())
		}()
	}
	wg.Wait()
	SortDevices(ds)
	return ds
}

// probe returns the build a Remoter relay at addr reports, or "".
func probe(ctx context.Context, client *http.Client, addr string) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/version", nil)
	if err != nil {
		return ""
	}
	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
	v := strings.TrimSpace(string(body))
	if resp.StatusCode != http.StatusOK || v == "" || strings.ContainsAny(v, "<\n") {
		return ""
	}
	return v
}

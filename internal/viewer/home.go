package viewer

import (
	"context"
	"net"
	"slices"
	"strconv"
	"time"

	"github.com/aleynikovich/remoter/internal/lan"
)

// HomeWait is how long to listen for answers from the home network.
const HomeWait = 1200 * time.Millisecond

// HomeDevices asks the home network which computers run Remoter. It finds
// nothing, without an error, when there is no home network.
func HomeDevices(ctx context.Context) []lan.Answer {
	as, _ := lan.Discover(ctx, HomeWait)
	return as
}

// MergeHome adds what answered on the home network to the tailnet devices.
// A device on both is reached over the home network, which is direct and
// works with Tailscale off; the others are listed as home devices of their
// own. Share-only answers are kept only for phones, which the viewer can
// mirror.
func MergeHome(ds []Device, home []lan.Answer) []Device {
	mine := lan.Addresses()
	for _, a := range home {
		if a.Remoter == "" && a.OS != "android" {
			continue
		}
		i := slices.IndexFunc(ds, func(d Device) bool {
			return a.Tailnet != "" && d.IP == a.Tailnet || d.LAN == a.IP
		})
		if i < 0 {
			ds = append(ds, Device{Name: a.Name, OS: a.OS, IP: a.IP, Self: slices.Contains(mine, a.IP)})
			i = len(ds) - 1
		}
		d := &ds[i]
		d.LAN, d.Online = a.IP, true
		if a.Port != 0 && strconv.Itoa(a.Port) != DefaultPort {
			d.Port = strconv.Itoa(a.Port)
		}
		if a.Remoter != "" {
			d.Remoter = a.Remoter
		}
		if d.Name == "" {
			d.Name = a.Name
		}
	}
	SortDevices(ds)
	return ds
}

// Addr is the server address to connect to the device: on the home
// network when it answered there, else over Tailscale.
func (d Device) Addr() string {
	ip := d.IP
	if d.LAN != "" {
		ip = d.LAN
	}
	return net.JoinHostPort(ip, cmpOr(d.Port, DefaultPort))
}

// Addrs lists every address the device has gone by, so a saved server or
// password from either network still matches it.
func (d Device) Addrs() []string {
	out := []string{d.Addr()}
	if t := net.JoinHostPort(d.IP, DefaultPort); t != out[0] {
		out = append(out, t)
	}
	return out
}

// Where says how the device is reached, for the device list.
func (d Device) Where() string {
	if d.LAN == "" {
		return d.IP
	}
	return d.LAN + " on the home network"
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

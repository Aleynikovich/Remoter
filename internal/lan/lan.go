// Package lan finds Remoter hosts on the home network without Tailscale.
//
// A viewer broadcasts a short UDP question to port 8080 (the same number as
// the relay's TCP port) and every Remoter on the network answers it
// directly with who it is. The viewer then connects to the address the
// answer came from.
package lan

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Port is the UDP port hosts listen on for questions.
const Port = 8080

// question is what viewers broadcast; anything else is ignored.
var question = []byte("REMOTER?")

// Answer is what a Remoter tells viewers about itself.
type Answer struct {
	Name    string `json:"name"`              // computer name
	OS      string `json:"os"`                // windows, linux, android...
	Port    int    `json:"port"`              // TCP port of its relay or share server
	Remoter string `json:"remoter,omitempty"` // build, when it can be controlled
	Share   bool   `json:"share,omitempty"`   // receives files and links
	Tailnet string `json:"tailnet,omitempty"` // its Tailscale IPv4, to match the tailnet list

	// IP is the address the answer came from; it is not sent.
	IP string `json:"-"`
}

// Addr is the TCP address to reach the computer at.
func (a Answer) Addr() string { return net.JoinHostPort(a.IP, strconv.Itoa(cmp.Or(a.Port, Port))) }

var tailnet = netip.MustParsePrefix("100.64.0.0/10")

// Local reports whether ip is on a home or office network: private,
// link-local or this computer. Tailscale's range does not count.
func Local(ip netip.Addr) bool {
	ip = ip.Unmap()
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast()
}

// TailscaleIP returns this computer's Tailscale IPv4 address, or "".
func TailscaleIP() string {
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok {
			if ip, ok := netip.AddrFromSlice(n.IP); ok && tailnet.Contains(ip.Unmap()) {
				return ip.Unmap().String()
			}
		}
	}
	return ""
}

// Addresses lists this computer's IPv4 addresses on local networks, for
// telling the user what to type into a viewer.
func Addresses() []string {
	var out []string
	ifs, _ := net.Interfaces()
	for _, ifc := range ifs {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			n, ok := a.(*net.IPNet)
			if !ok || n.IP.To4() == nil {
				continue
			}
			ip, _ := netip.AddrFromSlice(n.IP.To4())
			if Local(ip) && !ip.IsLinkLocalUnicast() {
				out = append(out, ip.String())
			}
		}
	}
	return out
}

// Serve answers viewers' questions on UDP port until ctx ends. who is asked
// for the answer each time, so it can change while running.
func Serve(ctx context.Context, port int, who func() Answer) error {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{Port: port})
	if err != nil {
		return err
	}
	go func() {
		<-ctx.Done()
		conn.Close()
	}()
	buf := make([]byte, 64)
	for {
		n, from, err := conn.ReadFromUDPAddrPort(buf)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			// Windows reports ICMP "port unreachable" from an earlier
			// answer as an error on the next read; carry on.
			if strings.Contains(err.Error(), "forcibly closed") {
				continue
			}
			return err
		}
		if !slices.Equal(buf[:n], question) || !Local(from.Addr()) {
			continue
		}
		b, _ := json.Marshal(who())
		conn.WriteToUDPAddrPort(b, from)
	}
}

// ServeInBackground runs Serve and logs why it stopped, if it did.
func ServeInBackground(ctx context.Context, port int, who func() Answer) {
	go func() {
		for {
			err := Serve(ctx, port, who)
			if ctx.Err() != nil {
				return
			}
			log.Printf("lan: not answering viewers on the home network (UDP %d): %v; retrying in 10 s", port, err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(10 * time.Second):
			}
		}
	}()
}

// broadcasts lists where to send the question: everyone on each local
// IPv4 network, and the all-networks broadcast address.
func broadcasts() []netip.Addr {
	out := []netip.Addr{netip.AddrFrom4([4]byte{255, 255, 255, 255})}
	ifs, _ := net.Interfaces()
	for _, ifc := range ifs {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			n, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip4, mask := n.IP.To4(), n.Mask
			if ip4 == nil || len(mask) != 4 {
				continue
			}
			ip, _ := netip.AddrFromSlice(ip4)
			if !ip.IsPrivate() {
				continue
			}
			var b [4]byte
			for i := range b {
				b[i] = ip4[i] | ^mask[i]
			}
			if bc := netip.AddrFrom4(b); !slices.Contains(out, bc) {
				out = append(out, bc)
			}
		}
	}
	return out
}

// extraTargets are asked too; tests use loopback.
var extraTargets []netip.Addr

// Discover asks the home network who runs Remoter and returns the answers
// that arrive within wait, one per computer.
func Discover(ctx context.Context, wait time.Duration) ([]Answer, error) {
	conn, err := net.ListenUDP("udp4", nil)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	go func() {
		<-ctx.Done()
		conn.SetReadDeadline(time.Now())
	}()

	// Ask twice in case the first packet is lost (Wi-Fi drops broadcasts
	// more than anything else).
	targets := append(broadcasts(), extraTargets...)
	var sendErr error
	var sent sync.WaitGroup
	sent.Add(1)
	go func() {
		defer sent.Done()
		for round := 0; round < 2; round++ {
			ok := false
			for _, t := range targets {
				if _, err := conn.WriteToUDPAddrPort(question, netip.AddrPortFrom(t, Port)); err == nil {
					ok = true
				} else if sendErr == nil {
					sendErr = err
				}
			}
			if ok {
				sendErr = nil
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait / 3):
			}
		}
	}()

	var answers []Answer
	buf := make([]byte, 2048)
	for {
		n, from, err := conn.ReadFromUDPAddrPort(buf)
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			continue
		}
		var a Answer
		if json.Unmarshal(buf[:n], &a) != nil || a.Name == "" && a.Remoter == "" && !a.Share {
			continue
		}
		a.IP = from.Addr().Unmap().String()
		if a.Port == 0 {
			a.Port = Port
		}
		// A computer on both cable and Wi-Fi answers on each; keep one.
		if !slices.ContainsFunc(answers, func(b Answer) bool { return b.IP == a.IP || b.Name == a.Name && b.OS == a.OS }) {
			answers = append(answers, a)
		}
	}
	sent.Wait()
	if len(answers) == 0 && sendErr != nil {
		return nil, sendErr
	}
	return answers, nil
}

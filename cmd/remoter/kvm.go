package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/netip"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/aleynikovich/remoter/internal/clipboard"
	"github.com/aleynikovich/remoter/internal/kvm"
	"github.com/aleynikovich/remoter/internal/viewer"
)

// runKVM is "remoter kvm": the merged desktop. This computer's keyboard and
// mouse also drive another computer running Remoter, which the pointer
// reaches by going past an edge of the screens here.
func runKVM(args []string) int {
	fs := flag.NewFlagSet("kvm", flag.ContinueOnError)
	side := fs.String("side", "bottom", "which edge of this computer's screens the other computer is past: bottom, top, left or right")
	screen := fs.Int("screen", -1, "only this screen's edge leads across (screens are numbered from 0, left to right); -1 for every screen's free part of the edge")
	push := fs.Float64("push", 50, "how far to keep pushing the mouse against the edge before the pointer crosses (mouse counts), so the taskbar there stays easy to reach; 0 crosses at once")
	speed := fs.Float64("speed", 1, "pointer speed on the other computer, relative to this one")
	password := fs.String("password", "", "the other computer's Remoter password (default: the one the viewer last used for it, else Remoter's default)")
	noClip := fs.Bool("no-clipboard", false, "do not share the clipboard")
	insecure := fs.Bool("insecure", false, "allow an address outside Tailscale: everything typed then crosses the network unencrypted")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), `Usage: remoter kvm [flags] computer

Use this computer's keyboard and mouse on another computer running Remoter,
as if its screens were past an edge of these: push the pointer off that
edge and it carries on over there, with the keyboard; move back over the
edge to come back. Scroll Lock brings them back from anywhere.
computer is a Tailscale device name or address (host or host:port).`)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return 2
	}
	s, err := kvm.ParseSide(*side)
	if err != nil {
		fmt.Fprintln(os.Stderr, "remoter kvm:", err)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	addr, err := kvmAddress(ctx, fs.Arg(0), *insecure)
	if err != nil {
		fmt.Fprintln(os.Stderr, "remoter kvm:", err)
		return 1
	}
	pw := *password
	if pw == "" {
		cfgPath, _ := viewer.ConfigPath()
		host, _, _ := net.SplitHostPort(addr)
		pw = viewer.LoadConfig(cfgPath).PasswordFor(addr, host)
	}
	if pw == "root" {
		log.Print("kvm: the other computer uses Remoter's default password; anyone who can reach it can type there. Consider setting -password on both.")
	}
	o := kvm.Options{
		Addr:     addr,
		Password: pw,
		Config:   kvm.Config{Side: s, Screen: *screen, Push: *push, Speed: *speed},
	}
	if !*noClip {
		o.Clipboard = clipboard.New()
	}
	log.Printf("kvm: Remoter %s, sending keyboard and mouse to %s past the %s edge", version(), addr, s)
	if err := kvm.Run(ctx, o); err != nil {
		fmt.Fprintln(os.Stderr, "remoter kvm:", err)
		return 1
	}
	return 0
}

// tailnet is Tailscale's address range, which WireGuard encrypts.
var tailnet = netip.MustParsePrefix("100.64.0.0/10")

// kvmAddress finds the other computer's relay address. Every keystroke
// crosses the network in Remoter's plain WebSocket, so it must be on the
// Tailscale network (encrypted) unless insecure is set.
func kvmAddress(ctx context.Context, name string, insecure bool) (string, error) {
	host, port, err := net.SplitHostPort(name)
	if err != nil {
		host, port = name, viewer.DefaultPort
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		ds, err := viewer.TailnetDevices(ctx)
		if err != nil {
			return "", fmt.Errorf("cannot look up %q on Tailscale: %v", host, err)
		}
		var found []viewer.Device
		for _, d := range ds {
			if d.Self {
				continue
			}
			if strings.EqualFold(d.Name, host) {
				found = []viewer.Device{d}
				break
			}
			if strings.Contains(strings.ToLower(d.Name), strings.ToLower(host)) {
				found = append(found, d)
			}
		}
		switch len(found) {
		case 0:
			return "", fmt.Errorf("no device called %q on Tailscale", host)
		case 1:
		default:
			var names []string
			for _, d := range found {
				names = append(names, d.Name)
			}
			return "", fmt.Errorf("%q matches several devices: %s", host, strings.Join(names, ", "))
		}
		if !found[0].Online {
			log.Printf("kvm: %s looks offline on Tailscale; trying anyway", found[0].Name)
		}
		if ip, err = netip.ParseAddr(found[0].IP); err != nil {
			return "", fmt.Errorf("%s has no usable address", found[0].Name)
		}
	}
	if !tailnet.Contains(ip.Unmap()) && !ip.IsLoopback() && !insecure {
		return "", errors.New("the merged desktop sends every keystroke, passwords included, without encryption of its own, so it only runs over Tailscale: use the computer's Tailscale name or 100.x address (or -insecure on a network you trust)")
	}
	return net.JoinHostPort(ip.String(), port), nil
}

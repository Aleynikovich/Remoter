package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/netip"
	"os"
	"os/signal"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/aleynikovich/remoter/internal/clipboard"
	"github.com/aleynikovich/remoter/internal/kvm"
	"github.com/aleynikovich/remoter/internal/lan"
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
computer is a Tailscale device name or address (host or host:port). Without
one it uses the computer from last time, or asks which one.`)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 1 {
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

	cfgPath, _ := viewer.ConfigPath()
	cfg := viewer.LoadConfig(cfgPath)
	target := fs.Arg(0)
	if target == "" {
		target = cfg.KVM
	}
	if target == "" {
		if target, err = chooseKVMTarget(ctx); err != nil {
			return kvmFail(err)
		}
	}
	addr, err := kvmAddress(ctx, target, *insecure)
	if err != nil {
		return kvmFail(err)
	}
	if cfg.KVM != target {
		cfg = viewer.LoadConfig(cfgPath)
		cfg.KVM = target
		if cfgPath != "" {
			cfg.Save(cfgPath)
		}
	}
	pw := *password
	if pw == "" {
		host, _, _ := net.SplitHostPort(addr)
		pw = cfg.PasswordFor(addr, host)
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
	log.Print("kvm: close this window (or press Ctrl+C) to stop")
	if err := kvm.Run(ctx, o); err != nil {
		return kvmFail(err)
	}
	return 0
}

// kvmFail reports err. Started from its shortcut the console window closes
// as soon as Remoter exits, so it waits for Enter first.
func kvmFail(err error) int {
	fmt.Fprintln(os.Stderr, "remoter kvm:", err)
	if isTerminal() {
		fmt.Fprint(os.Stderr, "Press Enter to close.")
		bufio.NewReader(os.Stdin).ReadString('\n')
	}
	return 1
}

// kvmTarget is a computer running Remoter that the keyboard and mouse can
// go to.
type kvmTarget struct {
	name, os, addr string
}

// kvmTargets lists the other computers running Remoter on the home
// network and on Tailscale. A computer on both is reached on the home
// network.
func kvmTargets(ctx context.Context) []kvmTarget {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	home := make(chan []lan.Answer, 1)
	go func() {
		as, _ := lan.Discover(ctx, 1500*time.Millisecond)
		home <- as
	}()
	var ts []kvmTarget
	if ds, err := viewer.TailnetDevices(ctx); err == nil {
		for _, d := range viewer.ProbeDevices(ctx, ds) {
			if !d.Self && d.Remoter != "" && !isPhoneOS(d.OS) {
				ts = append(ts, kvmTarget{d.Name, d.OS, d.Addr()})
			}
		}
	}
	self := lan.Addresses()
	for _, a := range <-home {
		if a.Remoter == "" || isPhoneOS(a.OS) || slices.Contains(self, a.IP) {
			continue
		}
		t := kvmTarget{a.Name, a.OS, a.Addr()}
		if i := slices.IndexFunc(ts, func(o kvmTarget) bool { return strings.EqualFold(o.name, a.Name) }); i >= 0 {
			ts[i] = t
		} else {
			ts = append(ts, t)
		}
	}
	return ts
}

func isPhoneOS(os string) bool {
	return strings.EqualFold(os, "android") || strings.EqualFold(os, "ios")
}

// chooseKVMTarget picks the other computer: the only one running Remoter,
// or the one chosen from a list.
func chooseKVMTarget(ctx context.Context) (string, error) {
	fmt.Println("Looking for computers running Remoter...")
	found := kvmTargets(ctx)
	switch len(found) {
	case 0:
		return "", errors.New("no other computer running Remoter answered, on the home network or on Tailscale")
	case 1:
		fmt.Printf("Using %s (%s).\n", found[0].name, found[0].addr)
		return found[0].name, nil
	}
	for i, t := range found {
		fmt.Printf("%d) %s (%s, %s)\n", i+1, t.name, t.os, t.addr)
	}
	fmt.Print("Use the keyboard and mouse on [1]: ")
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	n := 1
	if line = strings.TrimSpace(line); line != "" {
		var err error
		if n, err = strconv.Atoi(line); err != nil || n < 1 || n > len(found) {
			return "", errors.New("no such computer")
		}
	}
	return found[n-1].name, nil
}

// tailnet is Tailscale's address range, which WireGuard encrypts.
var tailnet = netip.MustParsePrefix("100.64.0.0/10")

// kvmAddress finds the other computer's relay address, by name on the home
// network or Tailscale, or as an address. Every keystroke crosses in
// Remoter's plain WebSocket, so it must be on Tailscale (encrypted) or the
// home network (with a warning), unless insecure is set.
func kvmAddress(ctx context.Context, name string, insecure bool) (string, error) {
	host, port, err := net.SplitHostPort(name)
	if err != nil {
		host, port = name, viewer.DefaultPort
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		var found []kvmTarget
		for _, t := range kvmTargets(ctx) {
			if strings.EqualFold(t.name, host) {
				found = []kvmTarget{t}
				break
			}
			if strings.Contains(strings.ToLower(t.name), strings.ToLower(host)) {
				found = append(found, t)
			}
		}
		switch len(found) {
		case 0:
			return "", fmt.Errorf("no computer called %q running Remoter answered, on the home network or on Tailscale; is Remoter running there? (its address works too)", host)
		case 1:
		default:
			var names []string
			for _, t := range found {
				names = append(names, t.name)
			}
			return "", fmt.Errorf("%q matches several computers: %s", host, strings.Join(names, ", "))
		}
		var p string
		if host, p, err = net.SplitHostPort(found[0].addr); err != nil {
			return "", err
		}
		if port == viewer.DefaultPort {
			port = p
		}
		if ip, err = netip.ParseAddr(host); err != nil {
			return "", fmt.Errorf("%s has no usable address", found[0].name)
		}
	}
	ip = ip.Unmap()
	switch {
	case tailnet.Contains(ip) || ip.IsLoopback():
	case lan.Local(ip):
		log.Print("kvm: on the home network, keystrokes (passwords too) are not encrypted; anyone on this network could read them. Tailscale encrypts them.")
	case !insecure:
		return "", errors.New("that address is outside the home network and Tailscale, and the merged desktop sends every keystroke without encryption of its own: use Tailscale, or -insecure if you trust the network")
	}
	return net.JoinHostPort(ip.String(), port), nil
}

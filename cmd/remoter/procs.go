package main

import (
	"path/filepath"
	"strings"
)

// Several Remoter processes can end up running on one computer: copies
// downloaded to different folders (each updating itself), the old separate
// host and viewer programs, or a copy that lost the race for the port and
// kept waiting for it. The rules below tell them apart by their command
// line, so the boot service, the launcher and the installer can stop the
// ones that should not be there.

type procRole int

const (
	roleOther   procRole = iota // helpers: -version, self-tests, send, -stop
	roleHost                    // shares this computer (relay and/or host)
	roleService                 // the boot service itself
	roleChild                   // the host the boot service started
	roleViewer                  // a viewer window
)

// remoterProc is a running Remoter found on this computer.
type remoterProc struct {
	pid  uint32
	path string   // the executable, "" if Windows would not say
	args []string // the command line including the program, nil if unknown
	role procRole
}

// isRemoterImage reports whether an executable name is one of Remoter's,
// old ones included (remoter-host.exe, remoter-viewer-windows-amd64.exe),
// but not its installer or uninstaller.
func isRemoterImage(name string) bool {
	n := strings.ToLower(filepath.Base(name))
	return strings.HasPrefix(n, "remoter") && strings.HasSuffix(n, ".exe") &&
		!strings.Contains(n, "setup") && !strings.Contains(n, "unins")
}

// roleOf classifies a Remoter by its command line.
func roleOf(argv []string) procRole {
	if len(argv) == 0 {
		return roleOther
	}
	if _, ok := viewerArgs(argv); ok {
		return roleViewer
	}
	if len(argv) == 1 {
		// Started from the desktop: it makes sure the computer is shared,
		// then stays open as the viewer window.
		return roleViewer
	}
	if argv[1] == "send" {
		return roleOther
	}
	role := roleHost
	for _, a := range argv[1:] {
		switch flagName(a) {
		case "service":
			return roleService
		case "service-host":
			role = roleChild
		case "version", "stop", "h264-selftest", "capture-selftest", "gpu-video-selftest",
			"install-service", "uninstall-service", "setup", "unsetup", "h", "help":
			return roleOther
		}
	}
	return role
}

// listenPort returns the port a host started with argv listens on.
func listenPort(argv []string) string {
	port := "8080"
	for i := 1; i < len(argv); i++ {
		a := argv[i]
		if flagName(a) != "addr" {
			continue
		}
		v, ok := strings.CutPrefix(strings.TrimLeft(a, "-"), "addr=")
		if !ok && i+1 < len(argv) {
			v = argv[i+1]
		}
		if p := portOf(v); p != "" {
			port = p[1:]
		}
	}
	return port
}

// portNum is the port of a listen address like ":8080".
func portNum(addr string) string {
	if p := portOf(addr); p != "" {
		return p[1:]
	}
	return "8080"
}

func flagName(a string) string {
	if !strings.HasPrefix(a, "-") {
		return ""
	}
	name := strings.TrimLeft(a, "-")
	if i := strings.IndexByte(name, '='); i >= 0 {
		name = name[:i]
	}
	return name
}

// strayHosts picks the hosts serving port that are not keep: when one
// Remoter is meant to serve, every other one is a leftover.
func strayHosts(procs []remoterProc, port string, keep ...uint32) []remoterProc {
	var out []remoterProc
next:
	for _, p := range procs {
		if p.role != roleHost && p.role != roleChild || listenPort(p.args) != port {
			continue
		}
		for _, k := range keep {
			if p.pid == k {
				continue next
			}
		}
		out = append(out, p)
	}
	return out
}

func (p remoterProc) describe() string {
	if p.path != "" {
		return p.path
	}
	return "a Remoter"
}

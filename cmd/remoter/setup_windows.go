package main

import (
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows/registry"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"

	"github.com/aleynikovich/remoter/internal/update"
)

// The Windows installer (installer/remoter.iss) puts Remoter in Program
// Files and then runs it with -setup, which makes that copy the only one:
// it points the boot service at it, stops every other Remoter (old
// downloads, the old separate host and viewer programs) and makes it the
// one that starts at sign-in. The uninstaller runs -unsetup.

const (
	installKey    = `SOFTWARE\Remoter`
	uninstallKey  = `SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\Remoter_is1`
	installedPath = "Path"
	redirectEnv   = "REMOTER_FROM_COPY"
)

// installedExe returns the Remoter the installer put in place, if any.
func installedExe() (string, bool) {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, installKey, registry.QUERY_VALUE)
	if err != nil {
		return "", false
	}
	defer k.Close()
	p, _, err := k.GetStringValue(installedPath)
	if err != nil || p == "" {
		return "", false
	}
	if _, err := os.Stat(p); err != nil {
		return "", false
	}
	return p, true
}

// redirectToInstalled hands a run of a downloaded copy over to the
// installed one, so a stale shortcut or a copy left in Downloads opens the
// installed Remoter instead of starting a second one. Helper runs (-version,
// which updates use to check a download, and the self-tests) are left
// alone.
func redirectToInstalled() {
	// Never more than one hop, even if the paths compare unequal for a
	// reason samePath misses.
	if os.Getenv(redirectEnv) != "" {
		return
	}
	role := roleOf(os.Args)
	if role != roleViewer && role != roleHost {
		return
	}
	installed, ok := installedExe()
	if !ok {
		return
	}
	self, err := update.Executable()
	if err != nil || samePath(self, installed) {
		return
	}
	args := os.Args[1:]
	if strings.Contains(strings.ToLower(filepath.Base(os.Args[0])), "viewer") {
		args = append([]string{"view"}, args...)
	}
	cmd := exec.Command(installed, args...)
	cmd.Env = append(os.Environ(), redirectEnv+"=1")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		return // run this copy instead
	}
	os.Exit(0)
}

func samePath(a, b string) bool {
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}

// setupLog logs to the installer's console and to Remoter's log.
func setupLog() {
	var w io.Writer = os.Stdout
	if f, err := openLog(); err == nil {
		w = io.MultiWriter(os.Stdout, f)
	}
	log.SetOutput(w)
	log.SetFlags(log.LstdFlags)
}

// runSetup makes this copy the one Remoter on the computer (-setup).
func runSetup() error {
	setupLog()
	if !isAdmin() {
		return fmt.Errorf("setting up Remoter needs administrator rights")
	}
	exe, err := update.Executable()
	if err != nil {
		return err
	}
	log.Printf("setting up Remoter %s at %s", version(), exe)

	// Point the boot service at this copy and stop it, so it starts again
	// from here below.
	if err := stopServiceFor(exe); err != nil {
		log.Printf("boot service: %v", err)
	}
	stopProcs(leftovers(listRemoters()), "replaced by the installed Remoter")

	// Starting at sign-in is the way in if the service ever fails.
	if err := setAutostart(true, exe, nil); err != nil {
		log.Printf("could not set up starting at sign-in: %v", err)
	}
	setUninstallVersion(version())

	if err := installService(); err != nil {
		log.Printf("could not install the boot service: %v", err)
	} else if err := startService(); err != nil {
		log.Printf("could not start the boot service: %v", err)
	}
	for range 60 {
		if v, ok := runningVersion(":8080"); ok {
			log.Printf("Remoter %s is sharing this computer", v)
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	// Never leave the computer unreachable: share it from here instead.
	log.Print("the boot service is not sharing this computer, starting Remoter directly")
	return startDetached(exe, []string{"-background"}, os.Environ())
}

// runUnsetup undoes runSetup before the uninstaller removes the files
// (-unsetup).
func runUnsetup() error {
	setupLog()
	if err := uninstallService(); err != nil {
		log.Printf("boot service: %v", err)
	}
	stopProcs(leftovers(listRemoters()), "Remoter is being uninstalled")
	exe, err := update.Executable()
	if err != nil {
		return err
	}
	// Only drop the sign-in entry if it starts this copy.
	if k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE|registry.SET_VALUE); err == nil {
		if v, _, err := k.GetStringValue(runValue); err == nil && strings.Contains(strings.ToLower(v), strings.ToLower(exe)) {
			k.DeleteValue(runValue)
		}
		k.Close()
	}
	return nil
}

// leftovers are all the other Remoters except short-lived helpers.
func leftovers(procs []remoterProc) []remoterProc {
	var out []remoterProc
	for _, p := range procs {
		if p.role != roleOther || p.args == nil {
			out = append(out, p)
		}
	}
	return out
}

// stopServiceFor points an installed boot service at exe and stops it.
func stopServiceFor(exe string) error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := m.OpenService(serviceName)
	if err != nil {
		return nil // not installed
	}
	defer s.Close()
	cfg, err := s.Config()
	if err != nil {
		return err
	}
	if want := syscall.EscapeArg(exe) + " -service"; cfg.BinaryPathName != want {
		log.Printf("boot service ran %s, now %s", cfg.BinaryPathName, want)
		cfg.BinaryPathName = want
		if err := s.UpdateConfig(cfg); err != nil {
			return err
		}
	}
	s.Control(svc.Stop)
	for range 50 {
		if st, err := s.Query(); err == nil && st.State == svc.Stopped {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("the old service did not stop")
}

func startService() error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := m.OpenService(serviceName)
	if err != nil {
		return err
	}
	defer s.Close()
	if st, err := s.Query(); err == nil && st.State == svc.Running {
		return nil
	}
	if err := s.Start(); err != nil {
		return err
	}
	return waitForRunning(s)
}

// setUninstallVersion keeps the version shown in Installed apps current as
// Remoter updates itself.
func setUninstallVersion(v string) {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, uninstallKey, registry.SET_VALUE)
	if err != nil {
		return
	}
	k.SetStringValue("DisplayVersion", v)
	k.Close()
}

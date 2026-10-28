//go:build windows

package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"

	"github.com/aleynikovich/remoter/internal/update"
)

// serviceSupported reports whether this build can install the boot service.
const serviceSupported = true

const (
	serviceName        = "Remoter"
	serviceDisplayName = "Remoter remote access"
	serviceDesc        = "Keeps Remoter running from boot, including at the Windows sign-in screen, so this computer can be reached remotely before anyone signs in."
)

// A login-time Run entry only starts Remoter once someone signs in. The
// service below runs as the system account from boot and keeps a full
// Remoter (relay and host) running in whatever session is on the screen,
// including the sign-in and lock screens. The host it starts follows the
// input desktop (see package wdesktop) and runs as the system account, so
// it can also control programs running as administrator, which a normal
// login-time copy cannot.
//
// The service never replaces the login Run entry: a copy started at login
// steps aside while the service is running (see deferToService) and takes
// over if the service is absent, so losing the service can never leave the
// computer unreachable once someone signs in.

// runService runs this process as the Windows service.
func runService() error {
	if f, err := openLog(); err == nil {
		log.SetOutput(f)
	}
	return svc.Run(serviceName, &remoterService{})
}

type remoterService struct{}

func (remoterService) Execute(args []string, req <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	status <- svc.Status{State: svc.StartPending}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go supervise(ctx)
	status <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	for c := range req {
		switch c.Cmd {
		case svc.Interrogate:
			status <- c.CurrentStatus
		case svc.Stop, svc.Shutdown:
			status <- svc.Status{State: svc.StopPending}
			cancel()
			return false, 0
		}
	}
	return false, 0
}

// supervise keeps one host copy running in the active console session,
// restarting it when the session changes, when the copy exits, or when an
// update has been installed. It is the only thing that starts and stops the
// copy, so there is never a second one fighting for the port.
func supervise(ctx context.Context) {
	log.Printf("Remoter service %s starting", version())
	exe, err := update.Executable()
	if err != nil {
		log.Printf("service cannot find its own path, nothing will start: %v", err)
		return
	}

	restart := make(chan struct{}, 1)
	if update.Version != "" {
		go serviceUpdater(ctx, exe, restart)
	}

	var child *childProc
	stopChild := func() {
		if child != nil {
			child.stop()
			child = nil
		}
	}
	defer stopChild()

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		session := activeConsoleSession()
		// Session 0 is the services' own session and has no desktop, and
		// 0xFFFFFFFF means no console is attached just now (briefly, during a
		// sign-in or a fast user switch). Neither can show a screen.
		ready := session != invalidSession && session != 0
		switch {
		case !ready:
			// Keep what we have; a copy already running keeps serving.
		case child != nil && child.session != session:
			log.Printf("console session changed %d -> %d, restarting host", child.session, session)
			stopChild()
		case child != nil && child.exited():
			log.Printf("host exited, restarting")
			stopChild()
		}
		if child == nil && ready {
			c, err := startHostInSession(exe, session)
			if err != nil {
				log.Printf("could not start host in session %d: %v", session, err)
			} else {
				child = c
				log.Printf("started host in session %d (pid %d)", session, c.pid)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-restart:
			log.Print("update installed, restarting host into the new version")
			stopChild()
		case <-ticker.C:
		}
	}
}

// serviceUpdater installs newer releases and asks supervise to restart the
// host into them. The host copies run with -no-update, so this is the only
// place updates happen, which keeps the single-restart rule above true.
func serviceUpdater(ctx context.Context, exe string, restart chan<- struct{}) {
	go update.Cleanup(exe)
	client := &http.Client{Timeout: 5 * time.Minute}
	c := &update.Checker{Current: update.Version, Asset: update.AssetName(), Client: client}
	c.API = os.Getenv("REMOTER_UPDATE_API")
	failed := ""
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Minute):
		}
		rel, asset, err := c.Latest(ctx)
		if err != nil {
			if ctx.Err() == nil {
				log.Printf("service update check failed: %v", err)
			}
			continue
		}
		if rel == nil || rel.Tag == failed {
			continue
		}
		log.Printf("service downloading %s", rel.Tag)
		if err := update.Install(ctx, client, exe, asset, rel.Tag); err != nil {
			log.Printf("service update to %s failed: %v", rel.Tag, err)
			failed = rel.Tag
			continue
		}
		log.Printf("service updated to %s", rel.Tag)
		select {
		case restart <- struct{}{}:
		default:
		}
	}
}

// installService installs and starts the boot service, then checks it
// reaches the running state. If anything fails it removes the half-installed
// service and returns an error, so the caller can keep the login Run entry
// as the way in.
func installService() error {
	if !isAdmin() {
		return fmt.Errorf("installing the service needs administrator rights")
	}
	exe, err := update.Executable()
	if err != nil {
		return err
	}
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()

	if s, err := m.OpenService(serviceName); err == nil {
		s.Close()
		return nil // already installed
	}
	s, err := m.CreateService(serviceName, exe, mgr.Config{
		DisplayName:  serviceDisplayName,
		Description:  serviceDesc,
		StartType:    mgr.StartAutomatic,
		ErrorControl: mgr.ErrorNormal,
	}, "-service")
	if err != nil {
		return err
	}
	// If the service process ever crashes, have Windows restart it, so the
	// computer does not silently become unreachable.
	s.SetRecoveryActions([]mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 30 * time.Second},
	}, 86400)

	if err := s.Start(); err != nil {
		s.Delete()
		s.Close()
		return fmt.Errorf("service installed but would not start: %w", err)
	}
	if err := waitForRunning(s); err != nil {
		s.Control(svc.Stop)
		s.Delete()
		s.Close()
		return fmt.Errorf("service did not come up: %w", err)
	}
	s.Close()
	return nil
}

// uninstallService stops and removes the boot service, leaving the login Run
// entry as the way Remoter starts.
func uninstallService() error {
	if !isAdmin() {
		return fmt.Errorf("removing the service needs administrator rights")
	}
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
	s.Control(svc.Stop)
	for range 25 {
		if st, err := s.Query(); err == nil && st.State == svc.Stopped {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	return s.Delete()
}

func waitForRunning(s *mgr.Service) error {
	for range 50 {
		st, err := s.Query()
		if err != nil {
			return err
		}
		if st.State == svc.Running {
			return nil
		}
		if st.State == svc.Stopped {
			return fmt.Errorf("service stopped right after starting")
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("timed out waiting for the service")
}

// serviceRunning reports whether the boot service is installed and running,
// which tells a login-time copy to step aside. It opens the service with
// query-only rights so a copy running as an ordinary user, before it has
// raised its own rights, can still tell.
func serviceRunning() bool {
	scm, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return false
	}
	defer windows.CloseServiceHandle(scm)
	name, err := windows.UTF16PtrFromString(serviceName)
	if err != nil {
		return false
	}
	s, err := windows.OpenService(scm, name, windows.SERVICE_QUERY_STATUS)
	if err != nil {
		return false // not installed, or no access
	}
	defer windows.CloseServiceHandle(s)
	var status windows.SERVICE_STATUS
	if err := windows.QueryServiceStatus(s, &status); err != nil {
		return false
	}
	return status.CurrentState == windows.SERVICE_RUNNING
}

// ensureService installs the boot service the first time Remoter runs with
// administrator rights, so "start at boot" takes effect on its own. It never
// reports an error up: if it cannot install, the login Run entry stays as
// the way in and nothing is lost.
func ensureService() {
	if !isAdmin() {
		return
	}
	m, err := mgr.Connect()
	if err != nil {
		return
	}
	s, err := m.OpenService(serviceName)
	m.Disconnect()
	if err == nil {
		s.Close()
		return // already installed
	}
	if err := installService(); err != nil {
		log.Printf("could not set Remoter to start at boot, it will still start when you sign in: %v", err)
		return
	}
	log.Print("Remoter now starts at boot (before sign-in) as a Windows service")
}

// --- launching the host into the console session as the system account ---

type childProc struct {
	handle  windows.Handle
	pid     uint32
	session uint32
}

func (c *childProc) exited() bool {
	if c == nil || c.handle == 0 {
		return true
	}
	ev, err := windows.WaitForSingleObject(c.handle, 0)
	return err == nil && ev == windows.WAIT_OBJECT_0
}

func (c *childProc) stop() {
	if c == nil || c.handle == 0 {
		return
	}
	// Ask it to exit cleanly via its loopback shutdown, then make sure.
	stopRunning(":8080")
	for range 15 {
		if c.exited() {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !c.exited() {
		windows.TerminateProcess(c.handle, 0)
	}
	windows.CloseHandle(c.handle)
	c.handle = 0
}

const invalidSession = 0xFFFFFFFF

func activeConsoleSession() uint32 {
	r, _, _ := procWTSGetActiveConsoleSessionId.Call()
	return uint32(r)
}

var (
	kernel32                         = windows.NewLazySystemDLL("kernel32.dll")
	procWTSGetActiveConsoleSessionId = kernel32.NewProc("WTSGetActiveConsoleSessionId")
)

// startHostInSession launches a full Remoter (relay and host) in the given
// session as the system account, on the Default desktop. The host then
// follows the input desktop itself, so it reaches the sign-in and lock
// screens. It runs with -no-update because the service does the updating.
func startHostInSession(exe string, session uint32) (*childProc, error) {
	token, err := systemTokenForSession(session)
	if err != nil {
		return nil, err
	}
	defer token.Close()

	args := []string{exe, "-mode", "all", "-background", "-no-update", "-service-host"}
	cmdline := strings.Join(quoteArgs(args), " ")
	argv, err := windows.UTF16PtrFromString(cmdline)
	if err != nil {
		return nil, err
	}
	appName, err := windows.UTF16PtrFromString(exe)
	if err != nil {
		return nil, err
	}
	desktop, err := windows.UTF16PtrFromString(`winsta0\default`)
	if err != nil {
		return nil, err
	}

	var si windows.StartupInfo
	si.Cb = uint32(unsafe.Sizeof(si))
	si.Desktop = desktop
	var pi windows.ProcessInformation

	const (
		createNoWindow       = 0x08000000
		detachedProcess      = 0x00000008
		createUnicodeEnviron = 0x00000400
	)
	var env *uint16
	if block, err := createEnvironment(token); err == nil && block != nil {
		env = block
		defer destroyEnvironment(block)
	}
	flags := uint32(createNoWindow | detachedProcess)
	if env != nil {
		flags |= createUnicodeEnviron
	}

	err = windows.CreateProcessAsUser(token, appName, argv, nil, nil, false,
		flags, env, nil, &si, &pi)
	if err != nil {
		return nil, err
	}
	windows.CloseHandle(pi.Thread)
	return &childProc{handle: pi.Process, pid: pi.ProcessId, session: session}, nil
}

// systemTokenForSession returns a primary token for the system account set
// to run in the given session, so a process created with it appears on that
// session's desktops.
func systemTokenForSession(session uint32) (windows.Token, error) {
	var self windows.Token
	err := windows.OpenProcessToken(windows.CurrentProcess(),
		windows.TOKEN_DUPLICATE|windows.TOKEN_QUERY|windows.TOKEN_ASSIGN_PRIMARY|windows.TOKEN_ADJUST_DEFAULT|windows.TOKEN_ADJUST_SESSIONID,
		&self)
	if err != nil {
		return 0, fmt.Errorf("open own token: %w", err)
	}
	defer self.Close()

	var dup windows.Token
	const maximumAllowed = 0x02000000
	err = windows.DuplicateTokenEx(self, maximumAllowed, nil,
		windows.SecurityIdentification, windows.TokenPrimary, &dup)
	if err != nil {
		return 0, fmt.Errorf("duplicate token: %w", err)
	}
	s := session
	err = windows.SetTokenInformation(dup, windows.TokenSessionId,
		(*byte)(unsafe.Pointer(&s)), uint32(unsafe.Sizeof(s)))
	if err != nil {
		dup.Close()
		return 0, fmt.Errorf("set token session: %w", err)
	}
	return dup, nil
}

var (
	userenv                     = windows.NewLazySystemDLL("userenv.dll")
	procCreateEnvironmentBlock  = userenv.NewProc("CreateEnvironmentBlock")
	procDestroyEnvironmentBlock = userenv.NewProc("DestroyEnvironmentBlock")
)

func createEnvironment(token windows.Token) (*uint16, error) {
	var block *uint16
	r, _, err := procCreateEnvironmentBlock.Call(uintptr(unsafe.Pointer(&block)), uintptr(token), 0)
	if r == 0 {
		return nil, err
	}
	return block, nil
}

func destroyEnvironment(block *uint16) {
	procDestroyEnvironmentBlock.Call(uintptr(unsafe.Pointer(block)))
}

func quoteArgs(args []string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		out[i] = syscall.EscapeArg(a)
	}
	return out
}

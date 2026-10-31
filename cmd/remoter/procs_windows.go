package main

import (
	"log"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

// listRemoters returns the other Remoter processes running on this
// computer. Ones this process may not look at (another user's, or the
// system's when this copy is not an administrator) come back without a
// command line and so with roleOther, which nothing stops.
func listRemoters() []remoterProc {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil
	}
	defer windows.CloseHandle(snap)
	self := uint32(os.Getpid())
	var out []remoterProc
	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	for err = windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		if e.ProcessID == self || !isRemoterImage(windows.UTF16ToString(e.ExeFile[:])) {
			continue
		}
		p := remoterProc{pid: e.ProcessID}
		if h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, e.ProcessID); err == nil {
			p.path = imagePath(h)
			p.args = commandLine(h)
			windows.CloseHandle(h)
		}
		p.role = roleOf(p.args)
		out = append(out, p)
	}
	return out
}

func imagePath(h windows.Handle) string {
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n := uint32(len(buf))
	if windows.QueryFullProcessImageName(h, 0, &buf[0], &n) != nil {
		return ""
	}
	return windows.UTF16ToString(buf[:n])
}

// commandLine reads another process's command line (Windows 8.1 and later).
func commandLine(h windows.Handle) []string {
	buf := make([]byte, 64<<10)
	var n uint32
	if windows.NtQueryInformationProcess(h, windows.ProcessCommandLineInformation,
		unsafe.Pointer(&buf[0]), uint32(len(buf)), &n) != nil {
		return nil
	}
	s := (*windows.NTUnicodeString)(unsafe.Pointer(&buf[0]))
	if s.Buffer == nil || s.Length == 0 {
		return nil
	}
	line := windows.UTF16PtrToString(s.Buffer)
	if int(s.Length)/2 < len(line) {
		line = line[:s.Length/2]
	}
	ptr, err := windows.UTF16PtrFromString(line)
	if err != nil {
		return nil
	}
	var argc int32
	argv, err := windows.CommandLineToArgv(ptr, &argc)
	if err != nil {
		return nil
	}
	defer windows.LocalFree(windows.Handle(uintptr(unsafe.Pointer(argv))))
	args := make([]string, argc)
	for i := range args {
		args[i] = windows.UTF16PtrToString(&argv[i][0])
	}
	return args
}

// stopProcs ends the given processes, saying why in the log. Ones this
// process has no right to end are left running.
func stopProcs(procs []remoterProc, why string) {
	for _, p := range procs {
		h, err := windows.OpenProcess(windows.PROCESS_TERMINATE|windows.SYNCHRONIZE, false, p.pid)
		if err != nil {
			log.Printf("could not stop %s (pid %d, %s): %v", p.describe(), p.pid, why, err)
			continue
		}
		if err := windows.TerminateProcess(h, 1); err == nil {
			windows.WaitForSingleObject(h, 5000)
			log.Printf("stopped %s (pid %d): %s", p.describe(), p.pid, why)
		}
		windows.CloseHandle(h)
	}
}

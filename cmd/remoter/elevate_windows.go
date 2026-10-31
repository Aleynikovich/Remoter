package main

import (
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const tokenElevationTypeLimited = 3 // an administrator's filtered token

// isAdmin reports whether this process runs with administrator rights.
// Windows drops input that a process without them sends to windows of
// processes that have them (installers, Task Manager, some games).
func isAdmin() bool { return windows.GetCurrentProcessToken().IsElevated() }

// canElevateSilently reports whether the user is an administrator whose
// Windows is set to never ask before raising a program's rights, so
// starting Remoter as administrator shows no prompt.
func canElevateSilently() bool {
	var typ uint32
	var n uint32
	err := windows.GetTokenInformation(windows.GetCurrentProcessToken(), windows.TokenElevationType,
		(*byte)(unsafe.Pointer(&typ)), uint32(unsafe.Sizeof(typ)), &n)
	if err != nil || typ != tokenElevationTypeLimited {
		return false
	}
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows\CurrentVersion\Policies\System`, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	v, _, err := k.GetIntegerValue("ConsentPromptBehaviorAdmin")
	return err == nil && v == 0
}

// startElevated starts exe with args as administrator, without a window.
func startElevated(exe string, args []string) error {
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = syscall.EscapeArg(a)
	}
	verb, _ := windows.UTF16PtrFromString("runas")
	file, err := windows.UTF16PtrFromString(exe)
	if err != nil {
		return err
	}
	params, err := windows.UTF16PtrFromString(strings.Join(quoted, " "))
	if err != nil {
		return err
	}
	return windows.ShellExecute(0, verb, file, params, nil, windows.SW_HIDE)
}

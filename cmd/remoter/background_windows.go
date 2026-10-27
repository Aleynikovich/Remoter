package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/windows/registry"
)

const backgroundSupported = true

const (
	detachedProcess = 0x00000008
	runKey          = `Software\Microsoft\Windows\CurrentVersion\Run`
	runValue        = "Remoter"
)

// startDetached starts exe with no console window, so it keeps running when
// the window it was started from closes.
func startDetached(exe string, args, env []string) error {
	cmd := exec.Command(exe, args...)
	cmd.Env = env
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: detachedProcess | syscall.CREATE_NEW_PROCESS_GROUP,
		HideWindow:    true,
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// setAutostart adds (or removes) Remoter from the programs Windows starts
// when the user logs in.
func setAutostart(on bool, exe string, args []string) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE|registry.QUERY_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if !on {
		err := k.DeleteValue(runValue)
		if err == registry.ErrNotExist {
			return nil
		}
		return err
	}
	cmd := []string{syscall.EscapeArg(exe)}
	for _, a := range append(args, "-background") {
		cmd = append(cmd, syscall.EscapeArg(a))
	}
	return k.SetStringValue(runValue, strings.Join(cmd, " "))
}

func logPath() string {
	dir := os.Getenv("LOCALAPPDATA")
	if dir == "" {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "Remoter", "remoter.log")
}

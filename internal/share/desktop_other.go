//go:build !linux && !windows

package share

import (
	"errors"
	"log"
	"os"
	"os/exec"
	"path/filepath"
)

func userHome() (string, error) { return os.UserHomeDir() }

func DownloadDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Downloads")
}

func Notify(n Notice) { log.Printf("share: %s: %s", n.Title, n.Body) }

func CopyText(text string) error { return errors.New("not supported on this system") }

func ReadClipboard() (string, error) { return "", errors.New("not supported on this system") }

func Open(target string) { exec.Command("open", target).Start() }

func ShowInFolder(path string) { Open(filepath.Dir(path)) }

package phone

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// SaveScreenshot writes a PNG to Pictures/Remoter and returns its path.
func SaveScreenshot(png []byte) (string, error) {
	dir := filepath.Join(picturesDir(), "Remoter")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "phone-"+time.Now().Format("2006-01-02_15-04-05")+".png")
	if err := os.WriteFile(path, png, 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// picturesDir is the user's Pictures folder, as the desktop names it.
func picturesDir() string {
	if out, err := exec.Command("xdg-user-dir", "PICTURES").Output(); err == nil {
		if d := strings.TrimSpace(string(out)); d != "" && d != os.Getenv("HOME") {
			return d
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return os.TempDir()
	}
	return filepath.Join(home, "Pictures")
}

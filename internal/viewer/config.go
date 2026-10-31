package viewer

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Config is what the viewer remembers between runs.
type Config struct {
	Server   string `json:"server"`
	ID       string `json:"id,omitempty"`
	Password string `json:"password,omitempty"`
	// Passwords remembers what worked for each server, for computers
	// that do not use the default one.
	Passwords map[string]string `json:"passwords,omitempty"`
	// Phone is the Tailscale address of the phone last mirrored, and
	// PhonePort its wireless debugging port then.
	Phone     string `json:"phone,omitempty"`
	PhonePort string `json:"phonePort,omitempty"`
	// Quality is the picture quality asked of hosts: auto, sharp, balanced
	// or smooth ("" is auto).
	Quality string `json:"quality,omitempty"`
	// SaverOnMetered turns data saver on by itself on a metered
	// connection, instead of asking.
	SaverOnMetered bool `json:"saverOnMetered,omitempty"`
}

// PasswordFor is the password to try for server: the one that worked last
// time, or the hosts' default.
// With several addresses for one computer, the first one known wins.
func (c Config) PasswordFor(server string, more ...string) string {
	for _, s := range append([]string{server}, more...) {
		if p, ok := c.Passwords[s]; ok {
			return p
		}
	}
	return "root"
}

// ConfigPath is ~/.config/remoter/viewer.json (or $XDG_CONFIG_HOME's).
func ConfigPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "remoter", "viewer.json"), nil
}

// LoadConfig reads the saved settings. A missing or broken file gives an
// empty Config.
func LoadConfig(path string) Config {
	var c Config
	if data, err := os.ReadFile(path); err == nil {
		json.Unmarshal(data, &c)
	}
	return c
}

// Save writes the settings, readable only by the user since they hold the
// password.
func (c Config) Save(path string) error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}

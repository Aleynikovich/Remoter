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

package phone

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// scrcpy's server, fetched from its GitHub release on first use and checked
// against this hash (the one Homebrew's scrcpy formula pins). Its version
// must match the protocol in msg.go and stream.go.
const (
	ServerVersion = "5.0.1"
	serverURL     = "https://github.com/Genymobile/scrcpy/releases/download/v" + ServerVersion + "/scrcpy-server-v" + ServerVersion
	serverSHA256  = "764eb6f79811d5211fe9df341120882ba9994c7a61b897d7bf3fb662e53bc536"
	// Where the server goes on the phone. scrcpy's own path is left alone
	// in case scrcpy is used too.
	serverRemote = "/data/local/tmp/remoter-scrcpy-server.jar"
)

// ServerPath returns the server file in the user's cache folder,
// downloading it there first if needed. $REMOTER_SCRCPY_SERVER overrides it.
func ServerPath(ctx context.Context) (string, error) {
	if p := os.Getenv("REMOTER_SCRCPY_SERVER"); p != "" {
		return p, nil
	}
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	dir = filepath.Join(dir, "remoter")
	path := filepath.Join(dir, "scrcpy-server-v"+ServerVersion)
	if data, err := os.ReadFile(path); err == nil && checksum(data) == serverSHA256 {
		return path, nil
	}
	if err := downloadServer(ctx, dir, path); err != nil {
		return "", fmt.Errorf("get scrcpy server %s: %w", ServerVersion, err)
	}
	return path, nil
}

func checksum(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func downloadServer(ctx context.Context, dir, path string) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, serverURL, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", serverURL, resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if checksum(data) != serverSHA256 {
		return errors.New("download does not match the expected checksum")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp := path + ".part"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

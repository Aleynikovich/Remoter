// Package update keeps a Remoter binary current: it looks for a newer build
// on the project's GitHub releases, downloads it next to the running
// executable and swaps it in. The caller then restarts into it.
package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Version is the release tag this binary was built as, stamped in by the
// release workflow with -ldflags "-X .../internal/update.Version=dev-12".
// Local builds leave it empty and never update themselves.
var Version = ""

// Repo is where releases are published.
const Repo = "aleynikovich/remoter"

// AssetName is the release file holding the binary for this platform.
func AssetName() string {
	name := fmt.Sprintf("remoter-%s-%s", runtime.GOOS, runtime.GOARCH)
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return name
}

// Release is the part of a GitHub release the updater reads.
type Release struct {
	Tag    string  `json:"tag_name"`
	Draft  bool    `json:"draft"`
	Assets []Asset `json:"assets"`
}

// Asset is a file attached to a release.
type Asset struct {
	Name   string `json:"name"`
	URL    string `json:"browser_download_url"`
	Size   int64  `json:"size"`
	Digest string `json:"digest"` // "sha256:<hex>", when GitHub provides it
}

// Checker finds newer releases.
type Checker struct {
	Current string // this binary's tag
	Asset   string // release file to look for
	API     string // releases endpoint; defaults to GitHub's for Repo
	Client  *http.Client

	etag   string
	cached *Release
}

// Latest returns the newest release that carries c.Asset, or nil when this
// binary is already the newest. Releases without the asset (builds from
// branches that predate the single binary) are skipped, so they can never
// replace an updating binary with one that cannot update.
func (c *Checker) Latest(ctx context.Context) (*Release, *Asset, error) {
	api := c.API
	if api == "" {
		api = "https://api.github.com/repos/" + Repo + "/releases?per_page=30"
	}
	client := c.Client
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, api, nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if c.etag != "" {
		// A 304 answer does not count against GitHub's rate limit.
		req.Header.Set("If-None-Match", c.etag)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()

	var newest *Release
	switch resp.StatusCode {
	case http.StatusNotModified:
		newest = c.cached
	case http.StatusOK:
		var releases []Release
		if err := json.NewDecoder(resp.Body).Decode(&releases); err != nil {
			return nil, nil, fmt.Errorf("read releases: %w", err)
		}
		// GitHub lists the newest release first.
		for i := range releases {
			if !releases[i].Draft && releases[i].asset(c.Asset) != nil {
				newest = &releases[i]
				break
			}
		}
		c.etag, c.cached = resp.Header.Get("ETag"), newest
	default:
		return nil, nil, fmt.Errorf("releases: %s", resp.Status)
	}
	if newest == nil || newest.Tag == c.Current || older(newest.Tag, c.Current) {
		return nil, nil, nil
	}
	return newest, newest.asset(c.Asset), nil
}

func (r *Release) asset(name string) *Asset {
	for i := range r.Assets {
		if r.Assets[i].Name == name {
			return &r.Assets[i]
		}
	}
	return nil
}

// older reports whether tag is a dev build numbered below current, which
// happens when the newest release is deleted. Other tags are not compared.
func older(tag, current string) bool {
	a, ok1 := devNumber(tag)
	b, ok2 := devNumber(current)
	return ok1 && ok2 && a < b
}

func devNumber(tag string) (int, bool) {
	n, err := strconv.Atoi(strings.TrimPrefix(tag, "dev-"))
	return n, err == nil && strings.HasPrefix(tag, "dev-")
}

// Install downloads asset, checks it, and swaps it in for the executable at
// exe. The running program keeps working (Windows allows renaming a running
// executable); it should restart to use the new version. tag is the release
// the asset belongs to: the new binary must report it from -version.
func Install(ctx context.Context, client *http.Client, exe string, asset *Asset, tag string) error {
	if client == nil {
		client = http.DefaultClient
	}
	newPath, oldPath := sidePaths(exe)
	os.Remove(newPath)
	if err := download(ctx, client, asset, newPath); err != nil {
		os.Remove(newPath)
		return err
	}
	if err := checkVersion(ctx, newPath, tag); err != nil {
		os.Remove(newPath)
		return err
	}
	if err := os.Remove(oldPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		os.Remove(newPath)
		return fmt.Errorf("remove previous version: %w", err)
	}
	if err := os.Rename(exe, oldPath); err != nil {
		os.Remove(newPath)
		return fmt.Errorf("move current version aside: %w", err)
	}
	if err := os.Rename(newPath, exe); err != nil {
		os.Rename(oldPath, exe)
		os.Remove(newPath)
		return fmt.Errorf("move new version in place: %w", err)
	}
	return nil
}

// sidePaths returns where the download goes and where the replaced binary
// is kept until the next start. Both keep the .exe extension so Windows
// treats them as programs.
func sidePaths(exe string) (newPath, oldPath string) {
	ext := filepath.Ext(exe)
	base := strings.TrimSuffix(exe, ext)
	return base + ".new" + ext, base + ".old" + ext
}

func download(ctx context.Context, client *http.Client, asset *Asset, dst string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, asset.URL, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: %s", asset.Name, resp.Status)
	}
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), resp.Body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("download %s: %w", asset.Name, err)
	}
	if asset.Size > 0 && n != asset.Size {
		return fmt.Errorf("download %s: got %d bytes, want %d", asset.Name, n, asset.Size)
	}
	if want, ok := strings.CutPrefix(asset.Digest, "sha256:"); ok && hex.EncodeToString(h.Sum(nil)) != want {
		return fmt.Errorf("download %s: checksum mismatch", asset.Name)
	}
	return nil
}

// checkVersion runs the downloaded binary with -version, which proves it
// starts on this machine (and was not blocked by antivirus) before it
// replaces the working one.
func checkVersion(ctx context.Context, path, tag string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "-version").Output()
	if err != nil {
		return fmt.Errorf("new version does not start: %w", err)
	}
	if got := strings.TrimSpace(string(out)); got != tag {
		return fmt.Errorf("new version reports %q, want %q", got, tag)
	}
	return nil
}

// Cleanup removes the binary replaced by the last update. The process that
// ran it may take a moment to exit, so it retries for a while.
func Cleanup(exe string) {
	_, oldPath := sidePaths(exe)
	for range 30 {
		err := os.Remove(oldPath)
		if err == nil || errors.Is(err, os.ErrNotExist) {
			return
		}
		time.Sleep(time.Second)
	}
}

// Executable returns the path of the running binary with symlinks resolved.
func Executable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(exe)
}

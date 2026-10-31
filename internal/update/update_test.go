package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestLatest(t *testing.T) {
	releases := []Release{
		{Tag: "dev-2", Assets: []Asset{{Name: "remoter.exe", URL: "u2"}}},         // GitHub lists some old ones first
		{Tag: "dev-9", Assets: []Asset{{Name: "remoter-host-windows-amd64.exe"}}}, // old layout
		{Tag: "dev-8", Draft: true, Assets: []Asset{{Name: "remoter.exe"}}},
		{Tag: "dev-7", Assets: []Asset{{Name: "remoter.exe", URL: "u7"}}},
		{Tag: "dev-6", Assets: []Asset{{Name: "remoter.exe", URL: "u6"}}},
	}
	hits, notModified := 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.Header.Get("If-None-Match") == `"v1"` {
			notModified++
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"v1"`)
		json.NewEncoder(w).Encode(releases)
	}))
	defer srv.Close()

	for _, tc := range []struct {
		current, want string
	}{
		{"dev-5", "dev-7"},
		{"dev-7", ""},
		{"dev-10", ""}, // never downgrade
		{"v1.0.0", "dev-7"},
	} {
		c := &Checker{Current: tc.current, Asset: "remoter.exe", API: srv.URL}
		for round := range 2 { // the second round is answered from the cache
			rel, asset, err := c.Latest(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			got := ""
			if rel != nil {
				got = rel.Tag
				if asset == nil || asset.URL != "u7" {
					t.Errorf("asset = %+v", asset)
				}
			}
			if got != tc.want {
				t.Errorf("current %s round %d: got %q, want %q", tc.current, round, got, tc.want)
			}
		}
	}
	if notModified != 4 || hits != 8 {
		t.Errorf("hits %d, not modified %d", hits, notModified)
	}
}

func TestInstall(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a shell script as the new binary")
	}
	script := []byte("#!/bin/sh\necho dev-7\n")
	sum := sha256.Sum256(script)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(script)
	}))
	defer srv.Close()

	dir := t.TempDir()
	exe := filepath.Join(dir, "remoter")
	if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	asset := &Asset{Name: "remoter", URL: srv.URL, Size: int64(len(script)), Digest: "sha256:" + hex.EncodeToString(sum[:])}

	if err := Install(context.Background(), nil, exe, asset, "dev-8"); err == nil {
		t.Fatal("installed a binary reporting the wrong version")
	}
	if b, _ := os.ReadFile(exe); string(b) != "old" {
		t.Fatal("failed install replaced the binary")
	}
	bad := *asset
	bad.Digest = "sha256:00"
	if err := Install(context.Background(), nil, exe, &bad, "dev-7"); err == nil {
		t.Fatal("installed a binary with the wrong checksum")
	}

	if err := Install(context.Background(), nil, exe, asset, "dev-7"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(exe); string(b) != string(script) {
		t.Fatalf("binary not replaced: %q", b)
	}
	newPath, oldPath := sidePaths(exe)
	if b, _ := os.ReadFile(oldPath); string(b) != "old" {
		t.Errorf("previous binary not kept: %q", b)
	}
	if _, err := os.Stat(newPath); !os.IsNotExist(err) {
		t.Errorf("download left behind: %v", err)
	}
	Cleanup(exe)
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Errorf("previous binary not cleaned up: %v", err)
	}
}

func TestSidePaths(t *testing.T) {
	n, o := sidePaths(`C:\Remoter\remoter.exe`)
	if n != `C:\Remoter\remoter.new.exe` || o != `C:\Remoter\remoter.old.exe` {
		t.Error(n, o)
	}
}

func TestOlder(t *testing.T) {
	for _, c := range []struct {
		tag, current string
		want         bool
	}{
		{"viewer-dev-3", "viewer-dev-12", true},
		{"viewer-dev-12", "viewer-dev-3", false},
		{"dev-3", "dev-12", true},
		{"dev-3", "viewer-dev-12", false}, // different series
		{"v1.0.0", "dev-2", false},
	} {
		if got := older(c.tag, c.current); got != c.want {
			t.Errorf("older(%q, %q) = %v, want %v", c.tag, c.current, got, c.want)
		}
	}
}

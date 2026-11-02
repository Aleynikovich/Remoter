package main

import (
	"context"
	"slices"
	"testing"
)

func TestViewerArgs(t *testing.T) {
	for _, tc := range []struct {
		argv   []string
		want   []string
		viewer bool
	}{
		{[]string{`C:\Users\a\remoter.exe`}, nil, false},
		{[]string{"remoter", "-password", "root"}, nil, false},
		{[]string{"remoter", "send", "f"}, nil, false},
		{[]string{"remoter", "view", "1.2.3.4:8080"}, []string{"1.2.3.4:8080"}, true},
		{[]string{"remoter", "phone", "1.2.3.4"}, []string{"phone", "1.2.3.4"}, true},
		{[]string{`C:\Downloads\remoter-viewer-windows-amd64.exe`, "-version"}, []string{"-version"}, true},
		{[]string{"/home/a/.local/bin/remoter-viewer"}, []string{}, true},
	} {
		got, ok := viewerArgs(tc.argv)
		if ok != tc.viewer || !slices.Equal(got, tc.want) {
			t.Errorf("%q: got %q %v", tc.argv, got, ok)
		}
	}
}

func TestKVMAddress(t *testing.T) {
	for in, want := range map[string]string{
		"100.116.112.42":      "100.116.112.42:8080",
		"100.116.112.42:9000": "100.116.112.42:9000",
		"127.0.0.1":           "127.0.0.1:8080",
	} {
		got, err := kvmAddress(context.Background(), in, false)
		if err != nil || got != want {
			t.Errorf("kvmAddress(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if got, err := kvmAddress(context.Background(), "192.168.1.20", false); err != nil || got != "192.168.1.20:8080" {
		t.Errorf("home network: %q, %v", got, err)
	}
	if _, err := kvmAddress(context.Background(), "8.8.8.8", false); err == nil {
		t.Error("an internet address was allowed without -insecure")
	}
	if got, err := kvmAddress(context.Background(), "8.8.8.8", true); err != nil || got != "8.8.8.8:8080" {
		t.Errorf("with -insecure: %q, %v", got, err)
	}
}

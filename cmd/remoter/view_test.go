package main

import (
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

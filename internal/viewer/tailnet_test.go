package viewer

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const sampleStatus = `{
  "BackendState": "Running",
  "Self": {"HostName": "hyprbox", "DNSName": "hyprbox.tail1234.ts.net.", "OS": "linux",
           "TailscaleIPs": ["100.64.0.2", "fd7a:115c:a1e0::2"], "Online": false},
  "Peer": {
    "nodekey:a": {"HostName": "", "DNSName": "gaming-pc.tail1234.ts.net.", "OS": "windows",
                  "TailscaleIPs": ["fd7a:115c:a1e0::3", "100.102.208.35"], "Online": true},
    "nodekey:b": {"HostName": "phone", "OS": "android", "TailscaleIPs": ["100.64.0.9"], "Online": false},
    "nodekey:c": {"HostName": "Attic", "OS": "linux", "TailscaleIPs": ["100.64.0.7"], "Online": true}
  }
}`

func TestParseTailscaleStatus(t *testing.T) {
	ds, err := parseTailscaleStatus([]byte(sampleStatus))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, d := range ds {
		got = append(got, d.Name+"="+d.IP)
	}
	// Online first by name (self counts as online), offline last.
	want := "Attic=100.64.0.7 gaming-pc=100.102.208.35 hyprbox=100.64.0.2 phone=100.64.0.9"
	if strings.Join(got, " ") != want {
		t.Errorf("got %v, want %s", got, want)
	}
	if !ds[2].Self || ds[1].OS != "windows" || ds[1].Addr() != "100.102.208.35:8080" {
		t.Errorf("fields wrong: %+v", ds)
	}

	if _, err := parseTailscaleStatus([]byte(`{"BackendState": "NeedsLogin"}`)); err == nil {
		t.Error("logged-out Tailscale gave no error")
	}
}

func TestProbe(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/version" {
			w.Write([]byte("dev-15\n"))
		}
	}))
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")
	if v := probe(context.Background(), srv.Client(), addr); v != "dev-15" {
		t.Errorf("probe = %q", v)
	}
	if v := probe(context.Background(), srv.Client(), "127.0.0.1:1"); v != "" {
		t.Errorf("closed port probe = %q", v)
	}
}

package main

import (
	"net/http"
	"testing"
)

func TestParseTailscaleStatus(t *testing.T) {
	status := `{"BackendState":"Running",
	  "Self":{"HostName":"aleydesk","OS":"windows","TailscaleIPs":["100.102.208.35","fd7a:115c:a1e0::1"],"Online":false},
	  "Peer":{"k1":{"HostName":"","DNSName":"phone.tail.ts.net.","OS":"android","TailscaleIPs":["100.1.2.3"],"Online":false},
	          "k2":{"HostName":"laptop","OS":"linux","TailscaleIPs":["100.1.2.4"],"Online":true}}}`
	got, err := parseTailscaleStatus([]byte(status))
	if err != nil {
		t.Fatal(err)
	}
	want := []device{
		{Name: "aleydesk", OS: "windows", IP: "100.102.208.35", Online: true, Self: true},
		{Name: "laptop", OS: "linux", IP: "100.1.2.4", Online: true},
		{Name: "phone", OS: "android", IP: "100.1.2.3"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("device %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if _, err := parseTailscaleStatus([]byte(`{"BackendState":"Stopped"}`)); err == nil {
		t.Error("a stopped Tailscale should be an error")
	}
}

func TestFromTailnet(t *testing.T) {
	for addr, want := range map[string]bool{
		"100.102.208.35:5000":      true,
		"127.0.0.1:5000":           true,
		"[fd7a:115c:a1e0::5]:5000": true,
		"192.168.1.20:5000":        false,
		"8.8.8.8:5000":             false,
	} {
		if got := fromTailnet(&http.Request{RemoteAddr: addr}); got != want {
			t.Errorf("%s: got %v, want %v", addr, got, want)
		}
	}
}

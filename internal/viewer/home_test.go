package viewer

import (
	"testing"

	"github.com/aleynikovich/remoter/internal/lan"
)

func TestMergeHomePrefersHomeNetwork(t *testing.T) {
	ds := []Device{
		{Name: "aleydesk", OS: "windows", IP: "100.102.208.35", Online: true},
		{Name: "laptop", OS: "linux", IP: "100.1.1.1"},
	}
	ds = MergeHome(ds, []lan.Answer{
		{Name: "aleydesk", OS: "windows", Port: 8080, Remoter: "dev-40", Tailnet: "100.102.208.35", IP: "192.168.1.20"},
		{Name: "media", OS: "linux", Port: 9000, Remoter: "dev-40", IP: "192.168.1.30"},
		{Name: "printer-pc", OS: "linux", Share: true, IP: "192.168.1.31"}, // share only: not listed
	})
	if len(ds) != 3 {
		t.Fatalf("got %d devices: %+v", len(ds), ds)
	}
	byName := map[string]Device{}
	for _, d := range ds {
		byName[d.Name] = d
	}
	desk := byName["aleydesk"]
	if desk.Addr() != "192.168.1.20:8080" || desk.Remoter != "dev-40" {
		t.Errorf("aleydesk = %+v, addr %s", desk, desk.Addr())
	}
	if got := desk.Addrs(); len(got) != 2 || got[1] != "100.102.208.35:8080" {
		t.Errorf("aleydesk addrs = %v", got)
	}
	if m := byName["media"]; m.Addr() != "192.168.1.30:9000" || !m.Online {
		t.Errorf("media = %+v, addr %s", m, m.Addr())
	}
	if l := byName["laptop"]; l.Addr() != "100.1.1.1:8080" {
		t.Errorf("laptop addr %s", l.Addr())
	}
}

package main

import "testing"

func TestRoleOf(t *testing.T) {
	for _, c := range []struct {
		argv []string
		want procRole
	}{
		{nil, roleOther},
		{[]string{`C:\Program Files\Remoter\remoter.exe`}, roleViewer},
		{[]string{"remoter.exe", "view", "100.1.2.3:8080"}, roleViewer},
		{[]string{"remoter-viewer-windows-amd64.exe"}, roleViewer},
		{[]string{"remoter.exe", "-background"}, roleHost},
		{[]string{"remoter-host.exe", "-server", "ws://x/ws/host"}, roleHost},
		{[]string{"remoter.exe", "-service"}, roleService},
		{[]string{"remoter.exe", "-mode", "all", "-background", "-no-update", "-service-host"}, roleChild},
		{[]string{"remoter.new.exe", "-version"}, roleOther},
		{[]string{"remoter.exe", "-stop"}, roleOther},
		{[]string{"remoter.exe", "send", "file.txt"}, roleOther},
		{[]string{"remoter.exe", "-setup"}, roleOther},
	} {
		if got := roleOf(c.argv); got != c.want {
			t.Errorf("roleOf(%q) = %d, want %d", c.argv, got, c.want)
		}
	}
}

func TestStrayHosts(t *testing.T) {
	procs := []remoterProc{
		{pid: 1, args: []string{"remoter.exe", "-service"}},
		{pid: 2, args: []string{"remoter.exe", "-service-host", "-background"}},
		{pid: 3, args: []string{`C:\Users\a\Downloads\remoter-windows-amd64.exe`, "-background"}},
		{pid: 4, args: []string{"remoter.exe", "-console", "-addr", ":18080"}},
		{pid: 5, args: []string{"remoter.exe", "-addr=:8080", "-background"}},
		{pid: 6, args: []string{"remoter.exe", "view"}},
		{pid: 7, args: nil},
	}
	for i := range procs {
		procs[i].role = roleOf(procs[i].args)
	}
	var got []uint32
	for _, p := range strayHosts(procs, "8080", 2) {
		got = append(got, p.pid)
	}
	if len(got) != 2 || got[0] != 3 || got[1] != 5 {
		t.Errorf("stray hosts = %v, want [3 5]", got)
	}
}

func TestIsRemoterImage(t *testing.T) {
	for name, want := range map[string]bool{
		"remoter.exe": true, "Remoter-Host.exe": true, "remoter-viewer-windows-amd64.exe": true,
		"RemoterSetup.exe": false, "unins000.exe": false, "remoter-unins.exe": false, "notepad.exe": false,
	} {
		if got := isRemoterImage(name); got != want {
			t.Errorf("isRemoterImage(%q) = %v", name, got)
		}
	}
}

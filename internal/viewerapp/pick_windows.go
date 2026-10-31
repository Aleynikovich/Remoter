package viewerapp

import (
	"errors"
	"os/exec"
	"strings"

	"github.com/aleynikovich/remoter/internal/update"
)

// pickFiles shows the Windows file dialog, through PowerShell so the viewer
// needs no COM or common-dialog bindings.
func pickFiles() ([]string, error) {
	script := `Add-Type -AssemblyName System.Windows.Forms
$d = New-Object System.Windows.Forms.OpenFileDialog
$d.Multiselect = $true
$d.Title = 'Send files with Remoter'
if ($d.ShowDialog() -eq 'OK') { $d.FileNames }`
	cmd := exec.Command("powershell", "-NoProfile", "-STA", "-NonInteractive", "-Command", script)
	update.HideWindow(cmd)
	out, err := cmd.Output()
	if err != nil {
		return nil, errors.New("could not open the file dialog: " + err.Error())
	}
	var paths []string
	for _, l := range strings.Split(string(out), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			paths = append(paths, l)
		}
	}
	if len(paths) == 0 {
		return nil, errors.New("no files chosen")
	}
	return paths, nil
}

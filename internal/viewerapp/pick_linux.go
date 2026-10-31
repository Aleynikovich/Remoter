package viewerapp

import (
	"errors"
	"os/exec"
	"strings"
)

// pickFiles asks for files with zenity or kdialog.
func pickFiles() ([]string, error) {
	var cmd *exec.Cmd
	switch {
	case have("zenity"):
		cmd = exec.Command("zenity", "--file-selection", "--multiple", "--separator=\n", "--title=Send files with Remoter")
	case have("kdialog"):
		cmd = exec.Command("kdialog", "--getopenfilename", ".", "--multiple", "--separate-output", "--title", "Send files with Remoter")
	default:
		return nil, errors.New("install zenity to pick files")
	}
	out, err := cmd.Output()
	if err != nil {
		return nil, errors.New("no files chosen")
	}
	var paths []string
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			paths = append(paths, l)
		}
	}
	if len(paths) == 0 {
		return nil, errors.New("no files chosen")
	}
	return paths, nil
}

func have(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

//go:build !linux && !windows

package viewerapp

import (
	"fmt"
	"os"
)

// Main reports that there is no desktop viewer for this system yet.
func Main(args []string) {
	fmt.Fprintln(os.Stderr, "the desktop viewer runs on Linux and Windows; open http://<computer>:8080 in a browser instead")
	os.Exit(1)
}

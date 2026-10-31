package main

import (
	"path/filepath"
	"strings"
)

// The desktop viewer is part of the same program, so one install and one
// version both shares this computer and controls others.

// viewerArgs reports whether this run is the desktop viewer, and the
// viewer's arguments: "remoter view ...", "remoter phone ..." (which the
// viewer starts for its phone window), or any run of a copy still named
// remoter-viewer, as the separate viewer used to be.
func viewerArgs(argv []string) ([]string, bool) {
	if strings.Contains(strings.ToLower(filepath.Base(argv[0])), "viewer") {
		return argv[1:], true
	}
	if len(argv) > 1 {
		switch argv[1] {
		case "view":
			return argv[2:], true
		case "phone", "phone-shot":
			return argv[1:], true
		}
	}
	return nil, false
}

// openViewer is set when Remoter was started from the desktop with no
// arguments on Windows: it then makes sure this computer is shared in the
// background, and opens the viewer window instead of the console text.
var openViewer bool

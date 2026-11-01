package main

import (
	"log"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/aleynikovich/remoter/internal/protocol"
)

// signinGuard stops remote keys from locking the Windows account at the
// sign-in screen. Windows counts every submitted wrong password, and after
// about ten it locks the account for a while. Remoter shows the sign-in
// screen (via the boot service), so keys meant for a window that is no
// longer there would land in the password box and, with Enter, count as
// failed sign-ins. On an account with no password any typed character is a
// wrong password.
//
// On the secure desktop (sign-in, lock and UAC screens) the guard therefore
// drops keys that type text, while Enter, Esc, Tab, arrows, Backspace,
// Ctrl+Alt+Del and the mouse keep working, and it lets through only a few
// Enter presses per window of time, so Remoter alone can never reach the
// lockout threshold.
type signinGuard struct {
	now     func() time.Time
	enters  []time.Time // recent Enter presses let through on the secure desktop
	dropped bool        // a "dropped" line was logged since the last allowed key
}

const (
	signinEnterLimit  = 3
	signinEnterWindow = 10 * time.Minute
)

// secureDesktop reports whether a desktop name is the one Windows uses for
// the sign-in, lock and UAC screens.
func secureDesktop(name string) bool { return strings.EqualFold(name, "Winlogon") }

// allow reports whether in may be applied on the given desktop.
func (g *signinGuard) allow(desktop string, in protocol.Input) bool {
	if !secureDesktop(desktop) || in.Kind != "keydown" {
		// Key releases always pass, so a key held before the screen
		// changed is never left stuck down.
		return true
	}
	if typesText(in) {
		g.drop("typing is off on the sign-in screen so it cannot lock the account")
		return false
	}
	if in.Code == "Enter" || in.Code == "NumpadEnter" || in.Key == "Enter" {
		now := time.Now()
		if g.now != nil {
			now = g.now()
		}
		kept := g.enters[:0]
		for _, t := range g.enters {
			if now.Sub(t) < signinEnterWindow {
				kept = append(kept, t)
			}
		}
		g.enters = kept
		if len(g.enters) >= signinEnterLimit {
			g.drop("too many Enter presses on the sign-in screen, waiting so the account does not lock")
			return false
		}
		g.enters = append(g.enters, now)
	}
	g.dropped = false
	return true
}

func (g *signinGuard) drop(why string) {
	if !g.dropped {
		log.Printf("input: %s", why)
		g.dropped = true
	}
}

// typesText reports whether a key press would type a character.
func typesText(in protocol.Input) bool {
	if in.Key != "" {
		r, size := utf8.DecodeRuneInString(in.Key)
		return size == len(in.Key) && r != utf8.RuneError && unicode.IsPrint(r)
	}
	c := in.Code
	switch {
	case strings.HasPrefix(c, "Key"), strings.HasPrefix(c, "Digit"):
		return true
	case strings.HasPrefix(c, "Numpad"):
		return c != "NumpadEnter"
	}
	switch c {
	case "Space", "Minus", "Equal", "BracketLeft", "BracketRight", "Backslash",
		"Semicolon", "Quote", "Comma", "Period", "Slash", "Backquote", "IntlBackslash":
		return true
	}
	return false
}

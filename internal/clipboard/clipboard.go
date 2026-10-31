// Package clipboard reads and writes the host's text clipboard.
package clipboard

import "strings"

// MaxText is the largest clipboard text shared in either direction. Larger
// copies are left alone rather than truncated.
const MaxText = 512 << 10

// Clipboard is a system text clipboard.
type Clipboard interface {
	// Seq changes whenever the clipboard content changes.
	Seq() uint32
	ReadText() (string, error)
	WriteText(string) error
}

// New returns the system clipboard, or nil where it is not supported.
func New() Clipboard {
	return newNative()
}

// toCRLF converts line endings to the Windows convention.
func toCRLF(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\n", "\r\n")
}

// toLF converts line endings to the convention browsers use.
func toLF(s string) string {
	return strings.ReplaceAll(s, "\r\n", "\n")
}

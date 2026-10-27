package clipboard

import "testing"

func TestLineEndings(t *testing.T) {
	if got := toCRLF("a\nb\r\nc"); got != "a\r\nb\r\nc" {
		t.Fatalf("toCRLF = %q", got)
	}
	if got := toLF("a\r\nb\nc"); got != "a\nb\nc" {
		t.Fatalf("toLF = %q", got)
	}
}

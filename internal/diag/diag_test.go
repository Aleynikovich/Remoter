package diag

import (
	"fmt"
	"strings"
	"testing"
)

func TestRingKeepsLastLines(t *testing.T) {
	r := &Ring{}
	for i := range Lines + 5 {
		fmt.Fprintf(r, "line %d\n", i)
	}
	fmt.Fprint(r, "half")
	got := strings.Split(r.String(), "\n")
	if len(got) != Lines || got[0] != "line 5" || got[len(got)-1] != fmt.Sprintf("line %d", Lines+4) {
		t.Fatalf("kept %d lines, first %q, last %q", len(got), got[0], got[len(got)-1])
	}
}

package diag

import (
	"fmt"
	"strings"
	"testing"
)

func TestRingKeepsFirstAndLastLines(t *testing.T) {
	r := &Ring{}
	n := StartLines + Lines + 5
	for i := range n {
		fmt.Fprintf(r, "line %d\n", i)
	}
	fmt.Fprint(r, "half")
	got := strings.Split(r.String(), "\n")
	if len(got) != StartLines+1+Lines || got[0] != "line 0" || got[StartLines] != "... 5 lines left out ..." ||
		got[StartLines+1] != fmt.Sprintf("line %d", StartLines+5) || got[len(got)-1] != fmt.Sprintf("line %d", n-1) {
		t.Fatalf("kept %d lines: %q ... %q", len(got), got[:3], got[len(got)-3:])
	}
}

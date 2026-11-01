package input

import (
	"testing"

	"github.com/aleynikovich/remoter/internal/protocol"
)

type record struct {
	Log
	got []protocol.Input
}

func (r *record) HandleInput(_ string, in protocol.Input) { r.got = append(r.got, in) }

func TestInRegionMapsPointer(t *testing.T) {
	rec := &record{}
	h := &InRegion{Handler: rec}
	h.HandleInput("v", protocol.Input{Kind: "mousemove", X: 0.5, Y: 0.5})
	h.SetRegion(&protocol.Region{X: 0.55, W: 0.45, H: 1})
	h.HandleInput("v", protocol.Input{Kind: "mousedown", X: 0.5, Y: 0.25})
	h.HandleInput("v", protocol.Input{Kind: "keydown", Code: "KeyA"})
	h.SetRegion(nil)
	h.HandleInput("v", protocol.Input{Kind: "wheel", X: 0.1, Y: 0.2})
	want := []struct{ x, y float64 }{{0.5, 0.5}, {0.775, 0.25}, {0, 0}, {0.1, 0.2}}
	for i, w := range want {
		if g := rec.got[i]; g.X != w.x || g.Y != w.y {
			t.Errorf("event %d (%s) at %v,%v, want %v,%v", i, g.Kind, g.X, g.Y, w.x, w.y)
		}
	}
}

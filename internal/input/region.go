package input

import (
	"sync"

	"github.com/aleynikovich/remoter/internal/protocol"
)

// InRegion passes events on to a Handler, mapping pointer positions from
// a region of the display (a virtual monitor, which is all the viewer sees)
// to the whole display.
type InRegion struct {
	Handler
	mu     sync.Mutex
	region *protocol.Region
}

// SetRegion sets the part of the display viewers see; nil means all of it.
func (r *InRegion) SetRegion(region *protocol.Region) {
	r.mu.Lock()
	r.region = region.Clean()
	r.mu.Unlock()
}

func (r *InRegion) HandleInput(viewer string, in protocol.Input) {
	switch in.Kind {
	case "mousemove", "mousedown", "mouseup", "wheel":
		r.mu.Lock()
		in.X, in.Y = r.region.Map(in.X, in.Y)
		r.mu.Unlock()
	}
	r.Handler.HandleInput(viewer, in)
}

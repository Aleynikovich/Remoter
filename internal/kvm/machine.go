package kvm

import (
	"image"
	"log"
	"sync"
	"time"

	"github.com/aleynikovich/remoter/internal/protocol"
)

// Config is what the user chooses.
type Config struct {
	// Side is the edge of this computer's screens the other one is past.
	Side Side
	// Screen, if 0 or more, is the only local screen whose edge leads
	// across; -1 means every screen's free part of that edge.
	Screen int
	// Push is how far the mouse must keep pushing against the edge before
	// the pointer crosses, in mouse counts, so the taskbar along that edge
	// stays easy to use. 0 crosses at once.
	Push float64
	// Speed multiplies pointer movement on the other computer.
	Speed float64
}

// Mouse event kinds the hook reports.
const (
	MouseMove = iota
	MouseDown
	MouseUp
	MouseWheel  // Data is the vertical wheel delta, 120 per notch, up positive
	MouseHWheel // Data is the horizontal wheel delta, right positive
)

// MouseEvent is what the low-level mouse hook saw.
type MouseEvent struct {
	Kind   int
	Pt     image.Point // where the pointer would be (screen pixels)
	Button int         // 0 left, 1 middle, 2 right, 3 back, 4 forward
	Data   int
	// Injected is set for input made by programs (Remoter's own host,
	// say), which is never sent across.
	Injected bool
}

// KeyEvent is what the low-level keyboard hook saw.
type KeyEvent struct {
	Down     bool
	Scan     uint32 // hardware scan code
	Extended bool   // the scan code had the 0xE0 prefix
	VK       uint32 // virtual key, to tell apart keys that share a scan code
	Injected bool
}

// Desk is the local side of the machine: the real pointer.
type Desk interface {
	// Warp puts the real pointer at p, in screen pixels.
	Warp(p image.Point)
}

// Link carries messages to the other computer. It must not block.
type Link interface {
	Send(m protocol.Message)
}

// Machine decides, for every event from the hooks, whether it stays on
// this computer or goes to the other one.
type Machine struct {
	cfg  Config
	desk Desk
	link Link
	// OnSwitch, if set, is called (without the lock) when input moves to
	// the other computer (remote set) or back.
	OnSwitch func(remote bool)

	mu     sync.Mutex
	geo    *geometry
	local  []image.Rectangle
	remote bool   // the other computer has the keyboard and mouse
	vpos   fpoint // its pointer, working space
	parkAt image.Point
	pushed float64   // how far the mouse pushed against the edge
	atEdge bool      // the pointer is on an exit segment
	edgeAt time.Time // last report there
	lastPt image.Point
	rawAt  time.Time // last raw mouse input
	pushAt time.Time // last raw movement towards the edge

	localKeys  map[uint32]bool // down on this computer when input left it
	remoteKeys map[uint32]bool // down on the other computer
	localBtns  map[int]bool
	remoteBtns map[int]bool
	pending    []func() // calls to make once the lock is released
}

// NewMachine returns a machine that is idle until SetScreens.
func NewMachine(cfg Config, desk Desk, link Link) *Machine {
	if cfg.Speed <= 0 {
		cfg.Speed = 1
	}
	return &Machine{
		cfg: cfg, desk: desk, link: link,
		localKeys: map[uint32]bool{}, remoteKeys: map[uint32]bool{},
		localBtns: map[int]bool{}, remoteBtns: map[int]bool{},
	}
}

// SetScreens sets both computers' screens. With remote nil the other
// computer is unreachable: input comes back here and stays.
func (m *Machine) SetScreens(local, remote []image.Rectangle) error {
	m.mu.Lock()
	defer m.unlock()
	m.local = local
	if remote == nil {
		m.geo = nil
		m.goLocalLocked(nil)
		return nil
	}
	g, err := newGeometry(m.cfg.Side, local, remote, m.cfg.Screen)
	if err != nil {
		m.geo = nil
		m.goLocalLocked(nil)
		return err
	}
	m.geo = g
	m.goLocalLocked(nil)
	return nil
}

// Remote reports whether the other computer has the keyboard and mouse.
func (m *Machine) Remote() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.remote
}

// unlock releases the lock and makes the calls queued under it.
func (m *Machine) unlock() {
	calls := m.pending
	m.pending = nil
	m.mu.Unlock()
	for _, f := range calls {
		f()
	}
}

func (m *Machine) later(f func()) { m.pending = append(m.pending, f) }

// Mouse handles a mouse event and reports whether to swallow it.
func (m *Machine) Mouse(e MouseEvent) bool {
	m.mu.Lock()
	defer m.unlock()
	if e.Injected {
		return false
	}
	if !m.remote {
		return m.localMouse(e)
	}
	switch e.Kind {
	case MouseMove:
		// The real pointer stays parked, so how far this event would
		// have moved it from there is the movement.
		d := m.geo.t.delta(fpoint{float64(e.Pt.X - m.parkAt.X), float64(e.Pt.Y - m.parkAt.Y)})
		d.X *= m.cfg.Speed
		d.Y *= m.cfg.Speed
		n, off := m.geo.move(m.vpos, d)
		if off && len(m.remoteBtns) == 0 {
			// Up past the top of the other computer: back here.
			m.goLocalLocked(&n)
			return true
		}
		if off {
			n.Y = m.vpos.Y // held button: stay on the other computer
		}
		m.vpos = n
		m.sendMove("mousemove", 0)
	case MouseDown, MouseUp:
		down := e.Kind == MouseDown
		if down {
			m.remoteBtns[e.Button] = true
		} else {
			delete(m.remoteBtns, e.Button)
		}
		m.sendMove(map[bool]string{true: "mousedown", false: "mouseup"}[down], e.Button)
	case MouseWheel, MouseHWheel:
		x, y := m.geo.normal(m.vpos)
		in := protocol.Input{Kind: "wheel", X: x, Y: y}
		if e.Kind == MouseWheel {
			// Windows: 120 a notch, up positive. Remoter: 100, down.
			in.DeltaY = -float64(e.Data) * 100 / 120
		} else {
			in.DeltaX = float64(e.Data) * 100 / 120
		}
		m.link.Send(protocol.Message{Type: protocol.TypeInput, Input: &in})
	}
	return true
}

func (m *Machine) localMouse(e MouseEvent) bool {
	switch e.Kind {
	case MouseDown:
		m.localBtns[e.Button] = true
	case MouseUp:
		delete(m.localBtns, e.Button)
	case MouseMove:
		if m.geo == nil || len(m.localBtns) > 0 {
			// Not while dragging something here: the drop would be lost.
			m.pushed = 0
			return false
		}
		p := m.geo.t.pt(e.Pt)
		last := m.lastPt
		m.lastPt = p
		s, ok := m.geo.exitAt(p)
		if !ok {
			m.atEdge, m.pushed = false, 0
			return false
		}
		now := time.Now()
		if !m.atEdge || now.Sub(m.edgeAt) > 300*time.Millisecond && now.Sub(m.rawAt) > time.Second {
			// Just got here, or rested here: start counting again, so
			// resting on the taskbar never crosses.
			m.pushed = 0
		}
		m.atEdge, m.edgeAt = true, now
		if now.Sub(m.rawAt) > time.Second {
			// No raw mouse input to measure the push with: count the
			// reports the pointer cannot follow, those that do not
			// slide it along the edge.
			if dx := p.X - last.X; dx >= -1 && dx <= 1 {
				m.pushed += pushPerReport
			}
		}
		if m.pushed < m.cfg.Push {
			return false
		}
		m.goRemoteLocked(s, p)
		return true
	}
	return false
}

// pushPerReport is how much one mouse report held against the edge counts
// towards Config.Push when raw input cannot measure it.
const pushPerReport = 4

// Raw counts raw mouse movement (mouse counts, unaccelerated, which go on
// while the pointer is held at the edge) towards the push needed to
// cross.
func (m *Machine) Raw(dx, dy int) {
	m.mu.Lock()
	defer m.unlock()
	m.rawAt = time.Now()
	if m.remote || m.geo == nil || !m.atEdge {
		return
	}
	d := m.geo.t.delta(fpoint{float64(dx), float64(dy)})
	switch {
	case d.Y > 0:
		if m.rawAt.Sub(m.pushAt) > 300*time.Millisecond {
			m.pushed = 0 // rested against the edge: start again
		}
		m.pushAt = m.rawAt
		m.pushed += d.Y
	case d.Y < 0:
		m.pushed = 0 // moving away from the edge
	}
	// Windows may not report a pointer that cannot move, so cross from
	// here too.
	if m.pushed >= m.cfg.Push && len(m.localBtns) == 0 {
		if s, ok := m.geo.exitAt(m.lastPt); ok {
			m.goRemoteLocked(s, m.lastPt)
		}
	}
}

func (m *Machine) goRemoteLocked(s segment, p image.Point) {
	m.remote = true
	m.pushed, m.atEdge = 0, false
	m.vpos = m.geo.entry(float64(p.X))
	// Park the real pointer a little in from the edge; movements are
	// measured from there. Warping inside the hook means the next report
	// is already measured from the new place.
	m.parkAt = m.geo.t.inv(m.geo.park(s)(p.X))
	clear(m.remoteKeys)
	clear(m.remoteBtns)
	at := m.parkAt
	m.later(func() { m.desk.Warp(at) })
	m.link.Send(protocol.Message{Type: protocol.TypeKVM, Text: "enter"})
	m.sendMove("mousemove", 0)
	if m.OnSwitch != nil {
		f := m.OnSwitch
		m.later(func() { f(true) })
	}
}

// goLocalLocked brings input back. off, if set, is where the remote
// pointer left the other computer's screens (working space); otherwise
// the real pointer stays where it is parked.
func (m *Machine) goLocalLocked(off *fpoint) {
	if !m.remote {
		return
	}
	m.remote = false
	m.pushed = 0
	m.link.Send(protocol.Message{Type: protocol.TypeKVM, Text: "leave"})
	clear(m.remoteKeys)
	clear(m.remoteBtns)
	if off != nil && m.geo != nil {
		at := m.geo.t.inv(m.geo.back(off.X))
		m.later(func() { m.desk.Warp(at) })
	}
	if m.OnSwitch != nil {
		f := m.OnSwitch
		m.later(func() { f(false) })
	}
}

// Return brings the keyboard and mouse back to this computer, as the
// hotkey does or when the other computer cannot be reached.
func (m *Machine) Return(why string) {
	m.mu.Lock()
	defer m.unlock()
	if m.remote {
		log.Printf("kvm: keyboard and mouse back here: %s", why)
		v := m.vpos
		m.goLocalLocked(&v)
	}
}

func (m *Machine) sendMove(kind string, button int) {
	x, y := m.geo.normal(m.vpos)
	m.link.Send(protocol.Message{Type: protocol.TypeInput, Input: &protocol.Input{Kind: kind, X: x, Y: y, Button: button}})
}

// Hotkey is the key that brings input back from the other computer: Scroll
// Lock, which hardly anything uses.
const hotkeyScan = 0x46

// Key handles a keyboard event and reports whether to swallow it.
func (m *Machine) Key(e KeyEvent) bool {
	m.mu.Lock()
	defer m.unlock()
	if e.Injected {
		return false
	}
	id := e.Scan
	if e.Extended {
		id |= 0xE000
	}
	if !m.remote {
		if e.Down {
			m.localKeys[id] = true
		} else {
			delete(m.localKeys, id)
		}
		return false
	}
	if !e.Down && m.localKeys[id] {
		// Pressed here before input left: let this computer see it go
		// up, or it would stay held here.
		delete(m.localKeys, id)
		return false
	}
	if id == hotkeyScan && !e.Extended && e.VK == 0x91 {
		if e.Down {
			v := m.vpos
			log.Print("kvm: Scroll Lock: keyboard and mouse back here")
			m.goLocalLocked(&v)
		}
		return true
	}
	code := KeyCode(e.Scan, e.Extended, e.VK)
	if code == "" {
		return true
	}
	if e.Down {
		if m.remoteKeys[id] {
			return true // auto-repeat: the other computer repeats by itself
		}
		m.remoteKeys[id] = true
	} else {
		if !m.remoteKeys[id] {
			return true
		}
		delete(m.remoteKeys, id)
	}
	kind := "keyup"
	if e.Down {
		kind = "keydown"
	}
	m.link.Send(protocol.Message{Type: protocol.TypeInput, Input: &protocol.Input{Kind: kind, Code: code}})
	return true
}

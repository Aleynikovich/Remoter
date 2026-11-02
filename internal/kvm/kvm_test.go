package kvm

import (
	"image"
	"math"
	"testing"

	"github.com/aleynikovich/remoter/internal/protocol"
)

type fakeDesk struct{ warps []image.Point }

func (d *fakeDesk) Warp(p image.Point) { d.warps = append(d.warps, p) }

type fakeLink struct{ msgs []protocol.Message }

func (l *fakeLink) Send(m protocol.Message) { l.msgs = append(l.msgs, m) }

func (l *fakeLink) take() []protocol.Message {
	m := l.msgs
	l.msgs = nil
	return m
}

func (l *fakeLink) lastMove(t *testing.T) protocol.Input {
	t.Helper()
	for i := len(l.msgs) - 1; i >= 0; i-- {
		if in := l.msgs[i].Input; in != nil && in.Kind == "mousemove" {
			return *in
		}
	}
	t.Fatalf("no mousemove in %+v", l.msgs)
	return protocol.Input{}
}

func near(a, b float64) bool { return math.Abs(a-b) < 0.01 }

func TestExitSegmentsSkipScreensBelow(t *testing.T) {
	screens := []image.Rectangle{
		image.Rect(0, 0, 5120, 1440),
		image.Rect(1000, 1440, 2920, 2520), // below part of the wide one
		image.Rect(5120, 0, 7040, 1080),
	}
	segs := exitSegments(screens, -1)
	want := []segment{
		{x0: 0, x1: 1000, y: 1439, screen: 0},
		{x0: 2920, x1: 5120, y: 1439, screen: 0},
		{x0: 1000, x1: 2920, y: 2519, screen: 1},
		{x0: 5120, x1: 7040, y: 1079, screen: 2},
	}
	if len(segs) != len(want) {
		t.Fatalf("segments %+v", segs)
	}
	for i := range want {
		if segs[i] != want[i] {
			t.Fatalf("segment %d is %+v, want %+v", i, segs[i], want[i])
		}
	}
	if only := exitSegments(screens, 2); len(only) != 1 || only[0].screen != 2 {
		t.Fatalf("only screen 2: %+v", only)
	}
}

func TestTransformsRoundTrip(t *testing.T) {
	for _, s := range []Side{Bottom, Top, Left, Right} {
		tr := transform(s)
		p := image.Pt(123, -45)
		if got := tr.inv(tr.pt(p)); got != p {
			t.Errorf("%v: %v -> %v", s, p, got)
		}
		r := image.Rect(10, 20, 110, 70)
		if tr.rect(r).Dx()*tr.rect(r).Dy() != r.Dx()*r.Dy() {
			t.Errorf("%v: rect %v became %v", s, r, tr.rect(r))
		}
	}
	// The chosen side becomes the bottom: moving towards it is +y.
	for s, d := range map[Side]fpoint{Bottom: {0, 1}, Top: {0, -1}, Left: {-1, 0}, Right: {1, 0}} {
		if got := transform(s).delta(d); got.Y != 1 || got.X != 0 {
			t.Errorf("%v: moving towards it gives %v", s, got)
		}
	}
}

func newTestMachine(t *testing.T, cfg Config, local, remote []image.Rectangle) (*Machine, *fakeDesk, *fakeLink) {
	t.Helper()
	d, l := &fakeDesk{}, &fakeLink{}
	m := NewMachine(cfg, d, l)
	if err := m.SetScreens(local, remote); err != nil {
		t.Fatal(err)
	}
	return m, d, l
}

func move(m *Machine, x, y int) bool { return m.Mouse(MouseEvent{Kind: MouseMove, Pt: image.Pt(x, y)}) }

func TestCrossDownAndBack(t *testing.T) {
	m, d, l := newTestMachine(t, Config{Screen: -1},
		[]image.Rectangle{image.Rect(0, 0, 1920, 1080)},
		[]image.Rectangle{image.Rect(0, 0, 2560, 1440)})

	if move(m, 960, 500) || m.Remote() {
		t.Fatal("crossed in the middle of the screen")
	}
	if !move(m, 960, 1079) || !m.Remote() {
		t.Fatal("did not cross at the bottom edge")
	}
	msgs := l.take()
	if msgs[0].Type != protocol.TypeKVM || msgs[0].Text != "enter" {
		t.Fatalf("first message %+v, want enter", msgs[0])
	}
	if in := msgs[1].Input; in == nil || !near(in.X, 0.5) || in.Y != 0 {
		t.Fatalf("entry %+v, want the middle of the top", msgs[1].Input)
	}
	park := d.warps[len(d.warps)-1]
	if park != image.Pt(960, 1079-parkGap) {
		t.Fatalf("parked at %v", park)
	}

	// Movements are measured from the park point and swallowed.
	if !move(m, park.X+100, park.Y+50) {
		t.Fatal("move on the other computer was not swallowed")
	}
	in := l.lastMove(t)
	if math.Abs(in.X*2559-1380) > 1 || !near(in.Y*1439, 50) {
		t.Fatalf("pointer at %.1f,%.1f", in.X*2559, in.Y*1439)
	}

	// Off the sides and bottom it stops at the edge.
	move(m, park.X, park.Y+5000)
	if in := l.lastMove(t); in.Y != 1 || !m.Remote() {
		t.Fatalf("pointer at y %v, remote %v", in.Y, m.Remote())
	}
	move(m, park.X, park.Y-5000+60)
	if m.Remote() {
		t.Fatal("did not come back over the top")
	}
	if msgs := l.take(); msgs[len(msgs)-1].Text != "leave" {
		t.Fatalf("last message %+v, want leave", msgs[len(msgs)-1])
	}
	back := d.warps[len(d.warps)-1]
	if back.Y != 1078 || math.Abs(float64(back.X)-1380*1920/2560.0) > 2 {
		t.Fatalf("came back at %v", back)
	}
}

func TestPushAgainstTheEdge(t *testing.T) {
	m, _, _ := newTestMachine(t, Config{Screen: -1, Push: 40},
		[]image.Rectangle{image.Rect(0, 0, 1920, 1080)},
		[]image.Rectangle{image.Rect(0, 0, 1920, 1080)})
	// Sliding along the taskbar does not cross.
	for x := 100; x < 1000; x += 10 {
		move(m, x, 1079)
	}
	if m.Remote() {
		t.Fatal("sliding along the edge crossed")
	}
	// Pushing on does, after a few reports.
	n := 0
	for !m.Remote() && n < 100 {
		move(m, 1000, 1079)
		n++
	}
	if n < 5 || n > 20 {
		t.Fatalf("crossed after %d reports", n)
	}

	// With raw input, its counts decide.
	m2, _, _ := newTestMachine(t, Config{Screen: -1, Push: 40},
		[]image.Rectangle{image.Rect(0, 0, 1920, 1080)},
		[]image.Rectangle{image.Rect(0, 0, 1920, 1080)})
	m2.Raw(0, 1)
	move(m2, 500, 1079)
	for range 20 {
		move(m2, 500, 1079)
	}
	if m2.Remote() {
		t.Fatal("crossed without raw movement towards the edge")
	}
	m2.Raw(0, 30)
	m2.Raw(0, 15)
	if !m2.Remote() {
		t.Fatal("45 counts of push did not cross")
	}
}

func TestNoCrossingWhileDragging(t *testing.T) {
	m, _, _ := newTestMachine(t, Config{Screen: -1},
		[]image.Rectangle{image.Rect(0, 0, 1920, 1080)},
		[]image.Rectangle{image.Rect(0, 0, 1920, 1080)})
	m.Mouse(MouseEvent{Kind: MouseDown, Pt: image.Pt(5, 5)})
	move(m, 960, 1079)
	if m.Remote() {
		t.Fatal("crossed while a button was held")
	}
	m.Mouse(MouseEvent{Kind: MouseUp, Pt: image.Pt(960, 1079)})
	move(m, 960, 1079)
	if !m.Remote() {
		t.Fatal("did not cross after the button went up")
	}
	// Injected input (Remoter's own host) is never taken.
	if m.Mouse(MouseEvent{Kind: MouseMove, Pt: image.Pt(1, 1), Injected: true}) {
		t.Fatal("swallowed injected input")
	}
}

func TestKeysFollowThePointer(t *testing.T) {
	m, _, l := newTestMachine(t, Config{Screen: -1},
		[]image.Rectangle{image.Rect(0, 0, 1920, 1080)},
		[]image.Rectangle{image.Rect(0, 0, 1920, 1080)})
	ctrl := KeyEvent{Down: true, Scan: 0x1D, VK: 0xA2}
	if m.Key(ctrl) {
		t.Fatal("swallowed a key on this computer")
	}
	move(m, 960, 1079)
	l.take()

	// Ctrl went down here, so it goes up here too.
	ctrl.Down = false
	if m.Key(ctrl) {
		t.Fatal("swallowed the release of a key held before crossing")
	}
	a := KeyEvent{Down: true, Scan: 0x1E, VK: 'A'}
	if !m.Key(a) || !m.Key(a) {
		t.Fatal("key on the other computer not swallowed")
	}
	a.Down = false
	m.Key(a)
	msgs := l.take()
	if len(msgs) != 2 || msgs[0].Input.Kind != "keydown" || msgs[0].Input.Code != "KeyA" || msgs[1].Input.Kind != "keyup" {
		t.Fatalf("sent %+v (auto-repeat should be dropped)", msgs)
	}

	// Scroll Lock brings everything back.
	m.Key(KeyEvent{Down: true, Scan: 0x45 + 1, VK: 0x91})
	if m.Remote() {
		t.Fatal("Scroll Lock did not bring input back")
	}
	if msgs := l.take(); len(msgs) != 1 || msgs[0].Text != "leave" {
		t.Fatalf("sent %+v, want leave", msgs)
	}
}

func TestOtherSidesAndLayouts(t *testing.T) {
	// The other computer is to the right of a 1920x1080 screen.
	m, d, l := newTestMachine(t, Config{Side: Right, Screen: -1},
		[]image.Rectangle{image.Rect(0, 0, 1920, 1080)},
		[]image.Rectangle{image.Rect(0, 0, 2560, 1440)})
	move(m, 1919, 540)
	if !m.Remote() {
		t.Fatal("did not cross the right edge")
	}
	in := l.lastMove(t)
	if in.X != 0 || !near(in.Y, 0.5) {
		t.Fatalf("entered at %v,%v, want the middle of the left edge", in.X, in.Y)
	}
	park := d.warps[0]
	if park != image.Pt(1919-parkGap, 540) {
		t.Fatalf("parked at %v", park)
	}
	move(m, park.X+200, park.Y) // further right
	if in := l.lastMove(t); !near(in.X*2559, 200) {
		t.Fatalf("x %v", in.X*2559)
	}
	move(m, park.X-300, park.Y) // back left past the edge
	if m.Remote() {
		t.Fatal("did not come back across the left edge")
	}

	// Two screens on the other computer, the second one lower: the top
	// of the higher one leads back, and moving across follows them.
	m, _, l = newTestMachine(t, Config{Screen: -1},
		[]image.Rectangle{image.Rect(0, 0, 3840, 1080)},
		[]image.Rectangle{image.Rect(0, 0, 1920, 1080), image.Rect(1920, 200, 3840, 1280)})
	move(m, 3000, 1079)
	if !m.Remote() {
		t.Fatal("did not cross")
	}
	in = l.lastMove(t)
	if !near(in.Y*1279, 200) {
		t.Fatalf("entered at y %.1f, want the top of the lower screen", in.Y*1279)
	}
}

func TestDisconnectBringsInputBack(t *testing.T) {
	m, _, l := newTestMachine(t, Config{Screen: -1},
		[]image.Rectangle{image.Rect(0, 0, 1920, 1080)},
		[]image.Rectangle{image.Rect(0, 0, 1920, 1080)})
	move(m, 960, 1079)
	m.SetScreens([]image.Rectangle{image.Rect(0, 0, 1920, 1080)}, nil)
	if m.Remote() {
		t.Fatal("input stayed away after the link dropped")
	}
	if move(m, 960, 1079) {
		t.Fatal("crossed with no other computer")
	}
	_ = l
}

func TestKeyCodes(t *testing.T) {
	cases := []struct {
		scan     uint32
		extended bool
		vk       uint32
		want     string
	}{
		{0x1E, false, 'A', "KeyA"},
		{0x1D, true, 0xA3, "ControlRight"},
		{0x38, true, 0xA5, "AltRight"},
		{0x21D, false, 0xA2, ""}, // AltGr's made-up Control
		{0x45, false, vkPause, "Pause"},
		{0x45, true, vkNumLock, "NumLock"},
		{0x5B, true, 0x5B, "MetaLeft"},
		{0x2A, true, 0xA0, ""}, // made-up Shift
		{0x48, false, 0x68, "Numpad8"},
		{0x48, true, 0x26, "ArrowUp"},
		{0x1C, true, 0x0D, "NumpadEnter"},
	}
	for _, c := range cases {
		if got := KeyCode(c.scan, c.extended, c.vk); got != c.want {
			t.Errorf("KeyCode(%#x, %v, %#x) = %q, want %q", c.scan, c.extended, c.vk, got, c.want)
		}
	}
}

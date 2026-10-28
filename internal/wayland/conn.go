//go:build linux

// Package wayland is a small Wayland client: just enough of the wire
// protocol for a Remoter host to capture outputs with wlr-screencopy and
// type and point with the virtual keyboard and pointer protocols, as
// Hyprland, Sway and other wlroots compositors offer them.
package wayland

import (
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"math"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Conn is a connection to a Wayland compositor.
type Conn struct {
	sock *net.UnixConn

	wmu sync.Mutex // serialises writes

	mu       sync.Mutex
	next     uint32
	free     []uint32
	handlers map[uint32]func(op uint16, r *reader)
	globals  map[uint32]Global
	outputs  []*Output
	err      error
	closed   chan struct{}

	registry uint32
	shm      uint32
	seat     uint32
	copier   Global // zwlr_screencopy_manager_v1
	pointers Global // zwlr_virtual_pointer_manager_v1
	keyboard Global // zwp_virtual_keyboard_manager_v1
}

// Global is an interface the compositor offers.
type Global struct {
	Name      uint32
	Interface string
	Version   uint32
	id        uint32 // our bound object, 0 when not bound
}

// Output is a monitor.
type Output struct {
	id      uint32
	global  uint32
	Name    string // DP-1, HDMI-A-1...
	X, Y    int    // position in the compositor's layout
	W, H    int    // current mode in pixels
	pending struct{ x, y, w, h int }
	ready   bool
}

// SocketPath finds the compositor's socket: $WAYLAND_DISPLAY, or, for a
// background service started without it, the only wayland-N socket in the
// runtime directory.
func SocketPath() (string, error) {
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		dir = fmt.Sprintf("/run/user/%d", os.Getuid())
	}
	if d := os.Getenv("WAYLAND_DISPLAY"); d != "" {
		if filepath.IsAbs(d) {
			return d, nil
		}
		return filepath.Join(dir, d), nil
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "wayland-*"))
	var socks []string
	for _, m := range matches {
		if st, err := os.Stat(m); err == nil && st.Mode()&os.ModeSocket != 0 {
			socks = append(socks, m)
		}
	}
	if len(socks) == 0 {
		return "", errors.New("no Wayland session is running")
	}
	sort.Strings(socks)
	return socks[0], nil
}

// Connect connects to the compositor and binds the globals Remoter uses.
func Connect() (*Conn, error) {
	path, err := SocketPath()
	if err != nil {
		return nil, err
	}
	sock, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, err
	}
	c := &Conn{
		sock:     sock,
		next:     2, // 1 is wl_display
		handlers: make(map[uint32]func(uint16, *reader)),
		globals:  make(map[uint32]Global),
		closed:   make(chan struct{}),
	}
	c.handlers[1] = c.displayEvent
	go c.readLoop()

	c.registry = c.newID(c.registryEvent)
	c.send(1, 1, c.registry) // wl_display.get_registry
	if err := c.Roundtrip(); err != nil {
		c.Close()
		return nil, err
	}
	// A second round trip collects the outputs' geometry and modes.
	if err := c.Roundtrip(); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

// Close disconnects.
func (c *Conn) Close() {
	c.fail(errors.New("wayland: connection closed"))
}

// Err returns why the connection ended, or nil while it is up.
func (c *Conn) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

func (c *Conn) fail(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return
	}
	c.err = err
	close(c.closed)
	c.sock.Close()
}

// Outputs lists the monitors, sorted by their position left to right.
func (c *Conn) Outputs() []Output {
	c.mu.Lock()
	defer c.mu.Unlock()
	var outs []Output
	for _, o := range c.outputs {
		if o.ready && o.W > 0 && o.H > 0 {
			outs = append(outs, *o)
		}
	}
	sort.SliceStable(outs, func(i, j int) bool {
		if outs[i].X != outs[j].X {
			return outs[i].X < outs[j].X
		}
		return outs[i].Y < outs[j].Y
	})
	return outs
}

// Has reports whether the compositor offers the interface.
func (c *Conn) Has(iface string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, g := range c.globals {
		if g.Interface == iface {
			return true
		}
	}
	return false
}

// Roundtrip waits until the compositor has handled every request sent so far.
func (c *Conn) Roundtrip() error {
	done := make(chan struct{})
	var once sync.Once
	cb := c.newID(func(op uint16, r *reader) { once.Do(func() { close(done) }) })
	c.send(1, 0, cb) // wl_display.sync
	select {
	case <-done:
		return nil
	case <-c.closed:
		return c.Err()
	case <-time.After(5 * time.Second):
		return errors.New("wayland: compositor did not answer")
	}
}

// newID allocates an object id with an event handler.
func (c *Conn) newID(h func(op uint16, r *reader)) uint32 {
	c.mu.Lock()
	defer c.mu.Unlock()
	var id uint32
	if n := len(c.free); n > 0 {
		id, c.free = c.free[n-1], c.free[:n-1]
	} else {
		id = c.next
		c.next++
	}
	if h == nil {
		h = func(uint16, *reader) {}
	}
	c.handlers[id] = h
	return id
}

func (c *Conn) setHandler(id uint32, h func(op uint16, r *reader)) {
	c.mu.Lock()
	c.handlers[id] = h
	c.mu.Unlock()
}

// fixed is a wl_fixed_t argument.
type fixed float64

// fd is a file descriptor argument, sent alongside the message.
type fd int

// send writes a request. Arguments are uint32 (also object and new ids),
// int32, fixed, string, []byte (array) or fd.
func (c *Conn) send(id uint32, op uint16, args ...any) error {
	b := make([]byte, 8, 64)
	var fds []int
	for _, a := range args {
		switch v := a.(type) {
		case uint32:
			b = binary.LittleEndian.AppendUint32(b, v)
		case int32:
			b = binary.LittleEndian.AppendUint32(b, uint32(v))
		case int:
			b = binary.LittleEndian.AppendUint32(b, uint32(int32(v)))
		case fixed:
			b = binary.LittleEndian.AppendUint32(b, uint32(int32(math.Round(float64(v)*256))))
		case string:
			b = binary.LittleEndian.AppendUint32(b, uint32(len(v)+1))
			b = append(b, v...)
			b = append(b, 0)
			for len(b)%4 != 0 {
				b = append(b, 0)
			}
		case []byte:
			b = binary.LittleEndian.AppendUint32(b, uint32(len(v)))
			b = append(b, v...)
			for len(b)%4 != 0 {
				b = append(b, 0)
			}
		case fd:
			fds = append(fds, int(v))
		default:
			panic(fmt.Sprintf("wayland: cannot send %T", a))
		}
	}
	binary.LittleEndian.PutUint32(b[0:], id)
	binary.LittleEndian.PutUint32(b[4:], uint32(len(b))<<16|uint32(op))
	var oob []byte
	if len(fds) > 0 {
		oob = syscall.UnixRights(fds...)
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if _, _, err := c.sock.WriteMsgUnix(b, oob, nil); err != nil {
		c.fail(err)
		return err
	}
	return nil
}

func (c *Conn) readLoop() {
	buf := make([]byte, 0, 64<<10)
	chunk := make([]byte, 32<<10)
	oob := make([]byte, syscall.CmsgSpace(28*4))
	for {
		n, oobn, _, _, err := c.sock.ReadMsgUnix(chunk, oob)
		if err != nil {
			c.fail(fmt.Errorf("wayland: %w", err))
			return
		}
		closeFDs(oob[:oobn]) // no event Remoter reads carries one
		buf = append(buf, chunk[:n]...)
		for len(buf) >= 8 {
			size := int(binary.LittleEndian.Uint32(buf[4:]) >> 16)
			if size < 8 {
				c.fail(errors.New("wayland: bad message from compositor"))
				return
			}
			if len(buf) < size {
				break
			}
			id := binary.LittleEndian.Uint32(buf)
			op := uint16(binary.LittleEndian.Uint32(buf[4:]))
			c.mu.Lock()
			h := c.handlers[id]
			c.mu.Unlock()
			if h != nil {
				h(op, &reader{b: buf[8:size]})
			}
			buf = buf[size:]
		}
		buf = append(buf[:0:0], buf...)
	}
}

func closeFDs(oob []byte) {
	msgs, err := syscall.ParseSocketControlMessage(oob)
	if err != nil {
		return
	}
	for _, m := range msgs {
		fds, err := syscall.ParseUnixRights(&m)
		if err != nil {
			continue
		}
		for _, f := range fds {
			syscall.Close(f)
		}
	}
}

func (c *Conn) displayEvent(op uint16, r *reader) {
	switch op {
	case 0: // error
		obj, code, msg := r.uint(), r.uint(), r.string()
		err := fmt.Errorf("wayland: protocol error on object %d (code %d): %s", obj, code, msg)
		log.Print(err)
		c.fail(err)
	case 1: // delete_id
		id := r.uint()
		c.mu.Lock()
		delete(c.handlers, id)
		c.free = append(c.free, id)
		c.mu.Unlock()
	}
}

func (c *Conn) registryEvent(op uint16, r *reader) {
	switch op {
	case 0: // global
		g := Global{Name: r.uint(), Interface: r.string(), Version: r.uint()}
		switch g.Interface {
		case "wl_shm":
			c.shm = c.bind(&g, 1, nil)
		case "wl_seat":
			if c.seat == 0 {
				c.seat = c.bind(&g, 1, nil)
			}
		case "wl_output":
			o := &Output{global: g.Name}
			o.id = c.bind(&g, 4, func(op uint16, r *reader) { c.outputEvent(o, op, r) })
			c.mu.Lock()
			c.outputs = append(c.outputs, o)
			c.mu.Unlock()
		case "zwlr_screencopy_manager_v1":
			c.bind(&g, 3, nil)
			c.copier = g
		case "zwlr_virtual_pointer_manager_v1":
			c.bind(&g, 2, nil)
			c.pointers = g
		case "zwp_virtual_keyboard_manager_v1":
			c.bind(&g, 1, nil)
			c.keyboard = g
		}
		c.mu.Lock()
		c.globals[g.Name] = g
		c.mu.Unlock()
	case 1: // global_remove
		name := r.uint()
		c.mu.Lock()
		delete(c.globals, name)
		for i, o := range c.outputs {
			if o.global == name {
				c.outputs = append(c.outputs[:i], c.outputs[i+1:]...)
				break
			}
		}
		c.mu.Unlock()
	}
}

// bind binds a global at the lower of its version and max.
func (c *Conn) bind(g *Global, max uint32, h func(uint16, *reader)) uint32 {
	g.Version = min(g.Version, max)
	g.id = c.newID(h)
	c.send(c.registry, 0, g.Name, g.Interface, g.Version, g.id)
	return g.id
}

func (c *Conn) outputEvent(o *Output, op uint16, r *reader) {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch op {
	case 0: // geometry: x, y, physical w, h, subpixel, make, model, transform
		o.pending.x, o.pending.y = int(r.int()), int(r.int())
	case 1: // mode: flags, width, height, refresh
		flags, w, h := r.uint(), r.int(), r.int()
		if flags&1 != 0 { // current
			o.pending.w, o.pending.h = int(w), int(h)
		}
	case 2: // done
		o.X, o.Y, o.W, o.H = o.pending.x, o.pending.y, o.pending.w, o.pending.h
		o.ready = true
	case 4: // name
		o.Name = r.string()
	}
}

// reader decodes event arguments.
type reader struct{ b []byte }

func (r *reader) uint() uint32 {
	if len(r.b) < 4 {
		return 0
	}
	v := binary.LittleEndian.Uint32(r.b)
	r.b = r.b[4:]
	return v
}

func (r *reader) int() int32 { return int32(r.uint()) }

func (r *reader) string() string {
	n := int(r.uint())
	if n == 0 || n > len(r.b) {
		return ""
	}
	s := strings.TrimRight(string(r.b[:n]), "\x00")
	r.b = r.b[min((n+3)&^3, len(r.b)):]
	return s
}

var shared struct {
	sync.Mutex
	c       *Conn
	tried   time.Time
	lastErr error
}

// Shared returns a connection shared by screen capture and input, opening
// it again after the compositor restarts. Attempts are spaced a few seconds
// apart so a host with no session to attach to does not spin.
func Shared() (*Conn, error) {
	shared.Lock()
	defer shared.Unlock()
	if shared.c != nil && shared.c.Err() == nil {
		return shared.c, nil
	}
	if time.Since(shared.tried) < 3*time.Second {
		if shared.lastErr == nil {
			shared.lastErr = errors.New("wayland: reconnecting")
		}
		return nil, shared.lastErr
	}
	shared.tried = time.Now()
	c, err := Connect()
	shared.c, shared.lastErr = c, err
	return c, err
}

// Available reports whether a Wayland session with screen capture is
// running, which is how a host tells Hyprland or Sway from X11.
func Available() bool {
	c, err := Shared()
	return err == nil && c.copier.id != 0
}

// Package relay implements the rendezvous and relay server.
//
// A host agent connects to /ws/host and is given a 9-digit session ID. A
// viewer connects to /ws/viewer and sends that ID with the session password.
// The server never sees the password check result in advance: it forwards the
// attempt to the host, and the host decides. After that, the server pipes
// screen frames from host to viewers and input events from viewers to host.
package relay

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/aleynikovich/remoter/internal/protocol"
)

const (
	authTimeout = 10 * time.Second
	// maxFailures wrong passwords for one ID lock that ID for lockout.
	maxFailures = 5
	lockout     = time.Minute
	// frameBuffer is how many screen updates may queue per viewer. Updates
	// only carry what changed, so none may be dropped; the host waits for acks
	// to stay within this, and a viewer that overflows it is disconnected
	// rather than allowed to stall the host.
	frameBuffer = 16
	maxMessage  = 16 << 20
	// maxViewerMessage leaves room for clipboard text.
	maxViewerMessage = 1 << 20
)

// Server is the relay. The zero value is not usable; call New.
type Server struct {
	mu       sync.Mutex
	hosts    map[string]*host
	failures map[string]*failure
	nextID   atomic.Uint64
}

type failure struct {
	count int
	until time.Time
}

type host struct {
	id   string
	conn *websocket.Conn

	mu      sync.Mutex
	viewers map[string]*viewer
	pending map[string]chan bool
}

type viewer struct {
	id      string
	conn    *websocket.Conn
	out     chan outgoing
	tooSlow bool // guarded by host.mu
}

type outgoing struct {
	typ  websocket.MessageType
	data []byte
}

// New returns an empty relay server.
func New() *Server {
	return &Server{
		hosts:    make(map[string]*host),
		failures: make(map[string]*failure),
	}
}

// Handler returns an http.Handler serving the WebSocket endpoints. Mount
// static files (the web viewer) alongside it.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/ws/host", s.serveHost)
	mux.HandleFunc("/ws/viewer", s.serveViewer)
	return mux
}

func (s *Server) serveHost(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	conn.SetReadLimit(maxMessage)
	defer conn.CloseNow()
	ctx := r.Context()

	var reg protocol.Message
	if err := readJSON(ctx, conn, &reg); err != nil || reg.Type != protocol.TypeRegister {
		conn.Close(websocket.StatusPolicyViolation, "expected register")
		return
	}

	h := &host{
		conn:    conn,
		viewers: make(map[string]*viewer),
		pending: make(map[string]chan bool),
	}
	if err := s.addHost(h); err != nil {
		conn.Close(websocket.StatusInternalError, err.Error())
		return
	}
	defer s.removeHost(h)
	log.Printf("host %s registered from %s", h.id, r.RemoteAddr)

	if err := writeJSON(ctx, conn, protocol.Message{Type: protocol.TypeRegistered, ID: h.id}); err != nil {
		return
	}

	for {
		typ, data, err := conn.Read(ctx)
		if err != nil {
			log.Printf("host %s gone: %v", h.id, closeReason(err))
			return
		}
		if typ == websocket.MessageBinary {
			h.broadcast(outgoing{typ, data})
			continue
		}
		var m protocol.Message
		if json.Unmarshal(data, &m) != nil {
			continue
		}
		switch {
		case protocol.ToViewers(m.Type):
			m.Viewer, m.Password = "", ""
			out, _ := json.Marshal(m)
			h.broadcast(outgoing{websocket.MessageText, out})
		case m.Type == protocol.TypeAuthResult:
			h.mu.Lock()
			ch := h.pending[m.Viewer]
			delete(h.pending, m.Viewer)
			h.mu.Unlock()
			if ch != nil {
				ch <- m.OK
			}
		}
	}
}

func (s *Server) serveViewer(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	conn.SetReadLimit(maxViewerMessage)
	defer conn.CloseNow()
	ctx := r.Context()

	var req protocol.Message
	if err := readJSON(ctx, conn, &req); err != nil || req.Type != protocol.TypeConnect {
		conn.Close(websocket.StatusPolicyViolation, "expected connect")
		return
	}

	fail := func(msg string) {
		writeJSON(ctx, conn, protocol.Message{Type: protocol.TypeError, Error: msg})
		conn.Close(websocket.StatusPolicyViolation, msg)
	}

	if req.ID == "" {
		// No ID: connect to the only shared computer, if there is one.
		id, err := s.onlyHost()
		if err != nil {
			fail(err.Error())
			return
		}
		req.ID = id
	}
	if s.locked(req.ID) {
		fail("too many attempts, try again later")
		return
	}
	h := s.host(req.ID)
	if h == nil {
		fail("no host with that ID is online")
		return
	}

	v := &viewer{
		id:   strconv.FormatUint(s.nextID.Add(1), 10),
		conn: conn,
		out:  make(chan outgoing, frameBuffer),
	}
	ok, err := h.authenticate(ctx, v.id, req.Password)
	if err != nil {
		fail("host did not answer")
		return
	}
	if !ok {
		s.recordFailure(req.ID)
		fail("wrong password")
		return
	}
	s.clearFailures(req.ID)

	if err := writeJSON(ctx, conn, protocol.Message{Type: protocol.TypeConnected, ID: h.id}); err != nil {
		return
	}
	h.addViewer(v)
	defer h.removeViewer(v)
	log.Printf("viewer %s connected to host %s from %s", v.id, h.id, r.RemoteAddr)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go v.pump(ctx)

	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			log.Printf("viewer %s left host %s: %v", v.id, h.id, closeReason(err))
			return
		}
		var m protocol.Message
		if json.Unmarshal(data, &m) != nil || !protocol.FromViewer(m.Type) {
			continue
		}
		if m.Type == protocol.TypeInput && m.Input == nil {
			continue
		}
		m.Viewer = v.id
		if err := writeJSON(ctx, h.conn, m); err != nil {
			return
		}
	}
}

func (s *Server) addHost(h *host) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for range 100 {
		n, err := rand.Int(rand.Reader, big.NewInt(900_000_000))
		if err != nil {
			return err
		}
		id := strconv.FormatInt(n.Int64()+100_000_000, 10)
		if _, taken := s.hosts[id]; !taken {
			h.id = id
			s.hosts[id] = h
			return nil
		}
	}
	return errors.New("could not allocate an ID")
}

func (s *Server) removeHost(h *host) {
	s.mu.Lock()
	delete(s.hosts, h.id)
	s.mu.Unlock()

	h.mu.Lock()
	defer h.mu.Unlock()
	for _, v := range h.viewers {
		v.conn.Close(websocket.StatusGoingAway, "host disconnected")
	}
	for _, ch := range h.pending {
		close(ch)
	}
}

func (s *Server) host(id string) *host {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hosts[id]
}

// onlyHost returns the ID of the single connected host.
func (s *Server) onlyHost() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch len(s.hosts) {
	case 0:
		return "", errors.New("no computer is being shared on this server")
	case 1:
		for id := range s.hosts {
			return id, nil
		}
	}
	return "", errors.New("several computers are shared here, enter the partner ID")
}

func (s *Server) locked(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.failures[id]
	return f != nil && time.Now().Before(f.until)
}

func (s *Server) recordFailure(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.failures[id]
	if f == nil {
		f = &failure{}
		s.failures[id] = f
	}
	f.count++
	if f.count >= maxFailures {
		f.count = 0
		f.until = time.Now().Add(lockout)
	}
}

func (s *Server) clearFailures(id string) {
	s.mu.Lock()
	delete(s.failures, id)
	s.mu.Unlock()
}

// authenticate asks the host to check a password and waits for its answer.
func (h *host) authenticate(ctx context.Context, viewerID, password string) (bool, error) {
	ch := make(chan bool, 1)
	h.mu.Lock()
	h.pending[viewerID] = ch
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.pending, viewerID)
		h.mu.Unlock()
	}()

	err := writeJSON(ctx, h.conn, protocol.Message{Type: protocol.TypeAuth, Viewer: viewerID, Password: password})
	if err != nil {
		return false, err
	}
	select {
	case ok, open := <-ch:
		if !open {
			return false, errors.New("host disconnected")
		}
		return ok, nil
	case <-time.After(authTimeout):
		return false, errors.New("timeout")
	case <-ctx.Done():
		return false, ctx.Err()
	}
}

func (h *host) addViewer(v *viewer) {
	h.mu.Lock()
	h.viewers[v.id] = v
	h.mu.Unlock()
}

func (h *host) removeViewer(v *viewer) {
	h.mu.Lock()
	_, ok := h.viewers[v.id]
	delete(h.viewers, v.id)
	h.mu.Unlock()
	if ok {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		writeJSON(ctx, h.conn, protocol.Message{Type: protocol.TypeViewerLeft, Viewer: v.id})
	}
}

// broadcast queues a message for every viewer. A viewer whose queue is full
// has stopped keeping up and is disconnected.
func (h *host) broadcast(msg outgoing) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, v := range h.viewers {
		if v.tooSlow {
			continue
		}
		select {
		case v.out <- msg:
		default:
			v.tooSlow = true
			log.Printf("viewer %s of host %s is too slow, disconnecting", v.id, h.id)
			go v.conn.Close(websocket.StatusTryAgainLater, "connection too slow")
		}
	}
}

func (v *viewer) pump(ctx context.Context) {
	for {
		select {
		case m := <-v.out:
			if err := v.conn.Write(ctx, m.typ, m.data); err != nil {
				return
			}
		case <-ctx.Done():
			return
		}
	}
}

func readJSON(ctx context.Context, c *websocket.Conn, m *protocol.Message) error {
	ctx, cancel := context.WithTimeout(ctx, authTimeout)
	defer cancel()
	typ, data, err := c.Read(ctx)
	if err != nil {
		return err
	}
	if typ != websocket.MessageText {
		return fmt.Errorf("expected text message")
	}
	return json.Unmarshal(data, m)
}

func writeJSON(ctx context.Context, c *websocket.Conn, m protocol.Message) error {
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return c.Write(ctx, websocket.MessageText, data)
}

func closeReason(err error) string {
	if s := websocket.CloseStatus(err); s != -1 {
		return s.String()
	}
	return err.Error()
}

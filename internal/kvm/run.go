package kvm

import (
	"context"
	"errors"
	"image"
	"log"
	"slices"
	"time"

	"github.com/aleynikovich/remoter/internal/clipboard"
	"github.com/aleynikovich/remoter/internal/protocol"
	"github.com/aleynikovich/remoter/internal/viewer"
)

// Options configure Run.
type Options struct {
	Addr     string // the other computer's Remoter, host:port
	Password string
	Config   Config
	// Clipboard, if set, is shared: this computer's text goes across each
	// time the pointer does, and the other computer's comes back as soon
	// as it changes.
	Clipboard clipboard.Clipboard
}

// ErrUnsupported is returned where the merged desktop cannot capture the
// keyboard and mouse.
var ErrUnsupported = errors.New("the merged desktop can only send from Windows so far")

// Run captures this computer's keyboard and mouse and sends them to the
// other computer whenever the pointer is past the chosen edge, until ctx
// ends. Input always comes back here when the link drops.
func Run(ctx context.Context, o Options) error {
	cap, err := newCapture()
	if err != nil {
		return err
	}
	return run(ctx, o, cap)
}

func run(ctx context.Context, o Options, cap capture) error {
	link := &chanLink{ch: make(chan protocol.Message, 8192)}
	m := NewMachine(o.Config, cap, link)
	local := cap.Screens()
	if len(local) == 0 {
		return errors.New("no screens found on this computer")
	}
	var clipSeq uint32
	if o.Clipboard != nil {
		clipSeq = o.Clipboard.Seq()
	}
	m.OnSwitch = func(remote bool) {
		if remote {
			log.Print("kvm: keyboard and mouse now on the other computer (Scroll Lock brings them back)")
		} else {
			log.Print("kvm: keyboard and mouse back on this computer")
		}
		if !remote || o.Clipboard == nil {
			return
		}
		// Lazy clipboard: what was copied here goes across with the
		// pointer.
		if seq := o.Clipboard.Seq(); seq != clipSeq {
			clipSeq = seq
			if text, err := o.Clipboard.ReadText(); err == nil && text != "" && len(text) <= clipboard.MaxText {
				link.Send(protocol.Message{Type: protocol.TypeClipboard, Text: text})
			}
		}
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	hookErr := make(chan error, 1)
	go func() {
		hookErr <- cap.Run(ctx, m)
		cancel()
	}()
	defer m.Return("stopping")

	// Input must come back here when this computer locks or shows a UAC
	// prompt: the hooks cannot see that desktop.
	go func() {
		t := time.NewTicker(300 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			if m.Remote() && !cap.OnUserDesktop() {
				m.Return("this computer locked or is asking for permission")
			}
		}
	}()

	var remote []image.Rectangle
	for ctx.Err() == nil {
		c, err := viewer.NewClient(o.Addr, "", o.Password)
		if err != nil {
			return err
		}
		cctx, stop := context.WithCancel(ctx)
		go c.Run(cctx)
		go link.pump(cctx, c)
		joined := time.Time{}
		connected, everConnected := false, false
		screens := time.NewTicker(2 * time.Second)
	session:
		for {
			select {
			case err := <-hookErr:
				stop()
				screens.Stop()
				return err
			case <-ctx.Done():
				break session
			case <-screens.C:
				// Monitors plugged in or out here, or no answer to the
				// join yet.
				if now := cap.Screens(); !slices.Equal(now, local) && len(now) > 0 {
					local = now
					if remote != nil {
						if err := m.SetScreens(local, remote); err != nil {
							log.Printf("kvm: %v", err)
						}
					}
				}
				if connected && remote == nil && time.Since(joined) > 8*time.Second {
					log.Print("kvm: the other computer did not answer; its Remoter may be too old for the merged desktop (it updates itself within minutes). Asking again.")
					c.Send(protocol.Message{Type: protocol.TypeKVM, Text: "join"})
					joined = time.Now()
				}
			case e := <-c.Events():
				switch e.Kind {
				case viewer.EventConnected:
					connected, everConnected = true, true
					log.Printf("kvm: connected to %s", o.Addr)
					c.Send(protocol.Message{Type: protocol.TypeKVM, Text: "join"})
					joined = time.Now()
				case viewer.EventStatus:
					log.Printf("kvm: %s", e.Text)
					connected, remote = false, nil
					m.SetScreens(local, nil)
				case viewer.EventEnded:
					if !everConnected && e.Text != unreachable {
						// Refused (a wrong password, say): retrying would
						// only get this computer locked out.
						stop()
						screens.Stop()
						return errors.New(e.Text)
					}
					log.Printf("kvm: %s", e.Text)
					connected, remote = false, nil
					m.SetScreens(local, nil)
					break session
				case viewer.EventMessage:
					switch e.Msg.Type {
					case protocol.TypeKVM:
						if e.Msg.Error != "" {
							stop()
							screens.Stop()
							return errors.New("the other computer: " + e.Msg.Error)
						}
						remote = nil
						for _, r := range e.Msg.Screens {
							remote = append(remote, image.Rect(r.X, r.Y, r.X+r.W, r.Y+r.H))
						}
						if err := m.SetScreens(local, remote); err != nil {
							log.Printf("kvm: %v", err)
							continue
						}
						log.Printf("kvm: ready: push the pointer past the %s edge to use the other computer (%d screens there)", o.Config.Side, len(remote))
					case protocol.TypeClipboard:
						if o.Clipboard != nil && e.Msg.Text != "" {
							if err := o.Clipboard.WriteText(e.Msg.Text); err == nil {
								clipSeq = o.Clipboard.Seq()
							}
						}
					}
				}
			}
		}
		stop()
		screens.Stop()
		select {
		case <-ctx.Done():
		case <-time.After(2 * time.Second):
		}
	}
	return nil
}

// unreachable is how viewer.Client ends when it never got through.
const unreachable = "Could not reach the server."

// chanLink queues messages from the hooks, which must never wait, and
// sends them from its own goroutine.
type chanLink struct {
	ch chan protocol.Message
}

func (l *chanLink) Send(m protocol.Message) {
	select {
	case l.ch <- m:
	default:
		// Only when the link is far behind; the host lets go of held
		// keys when input comes back or the link drops.
	}
}

func (l *chanLink) pump(ctx context.Context, c *viewer.Client) {
	for {
		select {
		case <-ctx.Done():
			return
		case m := <-l.ch:
			c.Send(m)
		}
	}
}

// capture is the platform's keyboard and mouse capture.
type capture interface {
	Desk
	// Run installs the hooks and feeds m until ctx ends.
	Run(ctx context.Context, m *Machine) error
	// Screens lists this computer's monitors in screen pixels.
	Screens() []image.Rectangle
	// OnUserDesktop reports whether the hooks can see input: false while
	// Windows shows the lock screen or a UAC prompt.
	OnUserDesktop() bool
}

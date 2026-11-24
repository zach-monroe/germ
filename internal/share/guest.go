package share

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// Guest is a joined session: Read yields the host's tmux control-mode
// output and Write sends tmux commands, so germ's tmux controller can run
// on it as if it had started tmux itself.
//
// If the connection drops (a network blip, or the host's own connection
// to the relay restarting), the guest reconnects for up to ReconnectFor.
// The host starts a fresh tmux client for it, so the stream restarts:
// Read then yields the line ReconnectMarker first, and the reader must
// forget any half-parsed state and request what it needs again.
type Guest struct {
	Welcome Welcome
	// OnPresence is called when the participant list changes.
	OnPresence func([]Participant)
	// OnDisconnect is called when the connection drops and reconnecting
	// starts.
	OnDisconnect func()
	// ReconnectFor is how long to keep trying after a drop.
	ReconnectFor time.Duration

	link      Link
	name      string
	client    string // stays the same across reconnects (Hello.Client)
	onWaiting func()
	box       *box
	pr        *io.PipeReader
	pw        *io.PipeWriter

	writeMu sync.Mutex
	mu      sync.Mutex
	conn    *websocket.Conn // nil while reconnecting
	closed  bool            // Close was called
	ended   bool            // the host said goodbye or removed us
	reason  string
	people  []Participant
}

// ReconnectMarker is the line Read yields when the stream restarts.
const ReconnectMarker = "%germ-reconnect"

// Join connects to a shared session. If the host has to approve the join,
// onWaiting (may be nil) is called while waiting.
func Join(ctx context.Context, link Link, name string, onWaiting func()) (*Guest, error) {
	g := &Guest{link: link, name: name, box: newBox(link.Key), ReconnectFor: 90 * time.Second,
		client: randomID(16), onWaiting: onWaiting}
	g.pr, g.pw = io.Pipe()
	c, w, err := g.handshake(ctx)
	if err != nil {
		return nil, err
	}
	g.Welcome = w
	g.conn = c
	go g.readLoop(c)
	go keepAlive(context.Background(), c)
	return g, nil
}

// handshake dials the relay, says hello, and waits for the welcome.
func (g *Guest) handshake(ctx context.Context) (*websocket.Conn, Welcome, error) {
	var w Welcome
	dctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	c, resp, err := websocket.Dial(dctx, g.link.websocketURL("guest"), nil)
	if err == nil {
		// Waiting for the host's approval can take a while; a host that
		// doesn't answer at all shouldn't.
		var rcancel context.CancelFunc
		dctx, rcancel = context.WithTimeout(ctx, 3*time.Minute)
		defer rcancel()
	}
	if err != nil {
		if resp != nil && resp.StatusCode == 404 {
			return nil, w, fmt.Errorf("that session isn't running (it may have ended)")
		}
		return nil, w, fmt.Errorf("connecting to relay %s: %w", g.link.Relay.Host, err)
	}
	c.SetReadLimit(maxMessage + 16)
	hello, _ := json.Marshal(Hello{Name: g.name, Version: protocolVersion, Client: g.client})
	answered := make(chan struct{})
	timer := time.AfterFunc(20*time.Second, func() {
		select {
		case <-answered:
		default:
			c.CloseNow() // no welcome and no "please wait"
		}
	})
	defer timer.Stop()
	if err := c.Write(dctx, websocket.MessageBinary, g.box.seal(append([]byte{msgHello}, hello...))); err != nil {
		c.CloseNow()
		return nil, w, err
	}
	for {
		_, data, err := c.Read(dctx)
		if err != nil {
			c.CloseNow()
			select {
			case <-answered:
			default:
				return nil, w, fmt.Errorf("the host didn't answer (wrong or old link?)")
			}
			if errors.Is(err, context.DeadlineExceeded) {
				return nil, w, fmt.Errorf("the host didn't let you in in time")
			}
			return nil, w, fmt.Errorf("joining: %s", closeReason(err))
		}
		msg, err := g.box.open(data)
		if err != nil || len(msg) == 0 {
			continue
		}
		switch msg[0] {
		case msgWaiting:
			select {
			case <-answered:
			default:
				close(answered)
				if g.onWaiting != nil {
					g.onWaiting()
				}
			}
		case msgWelcome:
			if err := json.Unmarshal(msg[1:], &w); err != nil {
				c.CloseNow()
				return nil, w, err
			}
			return c, w, nil
		case msgBye:
			c.CloseNow()
			return nil, w, fmt.Errorf("%s", msg[1:])
		}
	}
}

func closeReason(err error) string {
	var ce websocket.CloseError
	if errors.As(err, &ce) && ce.Reason != "" {
		return ce.Reason
	}
	return err.Error()
}

// permanent reports whether a closed connection means the session is over
// for us rather than a hiccup.
func permanent(err error) bool {
	var ce websocket.CloseError
	return errors.As(err, &ce) && ce.Code == websocket.StatusPolicyViolation // removed by the host
}

func (g *Guest) readLoop(c *websocket.Conn) {
	for {
		_, data, err := c.Read(context.Background())
		if err != nil {
			c.CloseNow()
			g.mu.Lock()
			g.conn = nil
			over := g.closed || g.ended || permanent(err)
			if g.reason == "" && over {
				g.reason = closeReason(err)
			}
			g.mu.Unlock()
			if over || g.ReconnectFor <= 0 {
				g.finish("")
				return
			}
			g.reconnect()
			return
		}
		msg, err := g.box.open(data)
		if err != nil || len(msg) == 0 {
			continue
		}
		switch msg[0] {
		case msgData:
			if _, err := g.pw.Write(msg[1:]); err != nil {
				return
			}
		case msgPresence:
			var ps []Participant
			if json.Unmarshal(msg[1:], &ps) == nil {
				g.mu.Lock()
				g.people = ps
				g.mu.Unlock()
				if g.OnPresence != nil {
					g.OnPresence(ps)
				}
			}
		case msgBye:
			g.mu.Lock()
			g.reason = string(msg[1:])
			g.ended = true
			g.mu.Unlock()
		}
	}
}

// finish ends the stream.
func (g *Guest) finish(reason string) {
	g.mu.Lock()
	if reason != "" && g.reason == "" {
		g.reason = reason
	}
	g.mu.Unlock()
	g.pw.Close()
}

// reconnect retries the handshake until it works or time runs out.
func (g *Guest) reconnect() {
	if g.OnDisconnect != nil {
		g.OnDisconnect()
	}
	deadline := time.Now().Add(g.ReconnectFor)
	wait := 500 * time.Millisecond
	for time.Now().Before(deadline) {
		g.mu.Lock()
		closed := g.closed
		g.mu.Unlock()
		if closed {
			g.finish("")
			return
		}
		time.Sleep(wait)
		wait = min(wait*2, 3*time.Second)
		c, _, err := g.handshake(context.Background())
		if err != nil {
			continue
		}
		// Writes may resume now (germ's controller drops its own until it
		// sees the marker). Tell the reader the stream restarts before
		// anything new arrives.
		g.mu.Lock()
		g.conn = c
		g.mu.Unlock()
		if _, err := g.pw.Write([]byte("\n" + ReconnectMarker + "\n")); err != nil {
			c.CloseNow()
			return
		}
		go g.readLoop(c)
		go keepAlive(context.Background(), c)
		return
	}
	g.finish("lost the connection to the shared session")
}

// Read returns the host's tmux output.
func (g *Guest) Read(p []byte) (int, error) { return g.pr.Read(p) }

// Write sends tmux commands to the host. While reconnecting, writes are
// dropped (the reader re-requests state after ReconnectMarker).
func (g *Guest) Write(p []byte) (int, error) {
	g.mu.Lock()
	c, closed := g.conn, g.closed
	g.mu.Unlock()
	if closed {
		return 0, io.ErrClosedPipe
	}
	if c == nil {
		return len(p), nil
	}
	g.writeMu.Lock()
	defer g.writeMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := c.Write(ctx, websocket.MessageBinary, g.box.seal(append([]byte{msgData}, p...))); err != nil {
		return len(p), nil // the reader notices and reconnects
	}
	return len(p), nil
}

// Close leaves the session.
func (g *Guest) Close() error {
	g.mu.Lock()
	g.closed = true
	c := g.conn
	g.mu.Unlock()
	if c != nil {
		return c.Close(websocket.StatusNormalClosure, "guest left")
	}
	return nil
}

// Reason says why the session ended, once Read has returned EOF.
func (g *Guest) Reason() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.reason
}

// People is the latest participant list.
func (g *Guest) People() []Participant {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]Participant(nil), g.people...)
}

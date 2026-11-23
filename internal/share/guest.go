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
type Guest struct {
	Welcome Welcome
	// OnPresence is called when the participant list changes.
	OnPresence func([]Participant)

	conn    *websocket.Conn
	box     *box
	pr      *io.PipeReader
	pw      *io.PipeWriter
	writeMu sync.Mutex
	mu      sync.Mutex
	reason  string
	people  []Participant
}

// Join connects to a shared session.
func Join(ctx context.Context, link Link, name string) (*Guest, error) {
	dctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	c, resp, err := websocket.Dial(dctx, link.websocketURL("guest"), nil)
	if err != nil {
		if resp != nil && resp.StatusCode == 404 {
			return nil, fmt.Errorf("that session isn't running (it may have ended)")
		}
		return nil, fmt.Errorf("connecting to relay %s: %w", link.Relay.Host, err)
	}
	c.SetReadLimit(maxMessage + 16)
	g := &Guest{conn: c, box: newBox(link.Key)}
	g.pr, g.pw = io.Pipe()
	hello, _ := json.Marshal(Hello{Name: name, Version: protocolVersion})
	if err := g.sendMsg(msgHello, hello); err != nil {
		c.CloseNow()
		return nil, err
	}
	// Wait for the welcome.
	for {
		_, data, err := c.Read(dctx)
		if err != nil {
			c.CloseNow()
			if errors.Is(err, context.DeadlineExceeded) {
				return nil, fmt.Errorf("the host didn't answer (wrong or old link?)")
			}
			return nil, fmt.Errorf("joining: %s", closeReason(err))
		}
		msg, err := g.box.open(data)
		if err != nil || len(msg) == 0 {
			continue
		}
		switch msg[0] {
		case msgWelcome:
			if err := json.Unmarshal(msg[1:], &g.Welcome); err != nil {
				c.CloseNow()
				return nil, err
			}
			go g.readLoop()
			go keepAlive(context.Background(), c)
			return g, nil
		case msgBye:
			c.CloseNow()
			return nil, fmt.Errorf("%s", msg[1:])
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

func (g *Guest) sendMsg(typ byte, body []byte) error {
	g.writeMu.Lock()
	defer g.writeMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return g.conn.Write(ctx, websocket.MessageBinary, g.box.seal(append([]byte{typ}, body...)))
}

func (g *Guest) readLoop() {
	for {
		_, data, err := g.conn.Read(context.Background())
		if err != nil {
			g.mu.Lock()
			if g.reason == "" {
				g.reason = closeReason(err)
			}
			g.mu.Unlock()
			g.pw.Close()
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
			g.mu.Unlock()
		}
	}
}

// Read returns the host's tmux output.
func (g *Guest) Read(p []byte) (int, error) { return g.pr.Read(p) }

// Write sends tmux commands to the host.
func (g *Guest) Write(p []byte) (int, error) {
	if err := g.sendMsg(msgData, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

// Close leaves the session.
func (g *Guest) Close() error {
	return g.conn.Close(websocket.StatusNormalClosure, "guest left")
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

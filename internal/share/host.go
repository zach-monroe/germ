package share

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// Host shares a tmux session: each guest gets its own tmux control-mode
// client, tunnelled through the relay.
type Host struct {
	Relay    *url.URL
	Session  string   // tmux session to share
	TmuxArgs []string // e.g. ["-L", "socket"]; empty uses $TMUX / default
	Name     string   // shown to guests, e.g. zach@laptop
	// OnChange is called (from a background goroutine) when guests come
	// and go.
	OnChange func([]Participant)
	// Log receives notable events.
	Log func(format string, args ...any)

	Room     string
	rwKey    Key
	roKey    Key
	rw, ro   *box
	conn     *websocket.Conn
	writeMu  sync.Mutex
	mu       sync.Mutex
	guests   map[uint32]*hostGuest
	closeErr error
}

type hostGuest struct {
	id        uint32
	box       *box
	readOnly  bool
	name      string
	tmux      *exec.Cmd
	stdin     io.WriteCloser
	pending   []byte // partial command line
	joined    time.Time
	session   string // the guest's own session, grouped with the host's
	sessionID string // its ID, e.g. "$4"
	typed     time.Time
	typing    bool
	// Blocked commands are logged at most every few seconds.
	lastBlockLog time.Time
	blocked      int
}

// NewHost prepares a share with fresh room and keys.
func NewHost(relay *url.URL, session string) (*Host, error) {
	rw, err := NewKey()
	if err != nil {
		return nil, err
	}
	ro, err := NewKey()
	if err != nil {
		return nil, err
	}
	return &Host{Relay: relay, Session: session, Room: randomID(12), rwKey: rw, roKey: ro,
		rw: newBox(rw), ro: newBox(ro), guests: map[uint32]*hostGuest{},
		Log: func(string, ...any) {}}, nil
}

// Links returns the invite links: one that can type, one that only watches.
func (h *Host) Links() (readWrite, readOnly Link) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return Link{Relay: h.Relay, Room: h.Room, Key: h.rwKey}, Link{Relay: h.Relay, Room: h.Room, Key: h.roKey}
}

// Run connects to the relay and serves guests until ctx ends or the
// connection drops.
func (h *Host) Run(ctx context.Context) error {
	link, _ := h.Links()
	c, _, err := websocket.Dial(ctx, link.websocketURL("host"), nil)
	if err != nil {
		return fmt.Errorf("connecting to relay %s: %w", h.Relay.Host, err)
	}
	c.SetReadLimit(maxMessage + 16)
	h.conn = c
	h.cleanupGuestSessions()
	go h.typingLoop(ctx)
	// Reads use their own context: cancelling a read's context closes the
	// connection, and stopping on purpose must first tell guests not to
	// reconnect.
	readCtx, stopReading := context.WithCancel(context.Background())
	defer stopReading()
	go func() {
		select {
		case <-ctx.Done():
		case <-readCtx.Done():
			return
		}
		h.mu.Lock()
		var gs []*hostGuest
		for _, g := range h.guests {
			if g.box != nil {
				gs = append(gs, g)
			}
		}
		h.mu.Unlock()
		for _, g := range gs {
			h.sendMsg(g, msgBye, []byte("the host stopped sharing"))
		}
		c.Close(websocket.StatusNormalClosure, "host stopped sharing")
	}()
	defer func() {
		h.mu.Lock()
		for _, g := range h.guests {
			h.stopGuest(g)
		}
		h.guests = map[uint32]*hostGuest{}
		h.mu.Unlock()
		c.Close(websocket.StatusNormalClosure, "host stopped sharing")
	}()
	go keepAlive(ctx, c)
	for {
		typ, data, err := c.Read(readCtx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("relay connection lost: %w", err)
		}
		if typ != websocket.MessageBinary || len(data) < 5 {
			continue
		}
		id := binary.BigEndian.Uint32(data[1:5])
		switch data[0] {
		case kindJoin:
			h.mu.Lock()
			h.guests[id] = &hostGuest{id: id, joined: time.Now()}
			h.mu.Unlock()
		case kindLeave:
			h.mu.Lock()
			g := h.guests[id]
			delete(h.guests, id)
			if g != nil {
				h.stopGuest(g)
			}
			h.mu.Unlock()
			if g != nil && g.name != "" {
				h.Log("%s left", g.name)
				h.changed()
			}
		case kindData:
			h.fromGuest(id, data[5:])
		}
	}
}

func (h *Host) send(id uint32, kind byte, payload []byte) error {
	h.writeMu.Lock()
	defer h.writeMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return h.conn.Write(ctx, websocket.MessageBinary, frame(kind, id, payload))
}

func (h *Host) sendMsg(g *hostGuest, typ byte, body []byte) error {
	return h.send(g.id, kindData, g.box.seal(append([]byte{typ}, body...)))
}

func (h *Host) kick(id uint32) { h.send(id, kindKick, nil) }

func (h *Host) fromGuest(id uint32, data []byte) {
	h.mu.Lock()
	g := h.guests[id]
	h.mu.Unlock()
	if g == nil {
		return
	}
	if g.box == nil {
		// The first message decides which link the guest used.
		h.mu.Lock()
		rwBox, roBox := h.rw, h.ro
		h.mu.Unlock()
		if msg, err := rwBox.open(data); err == nil {
			h.mu.Lock()
			g.box = rwBox
			h.mu.Unlock()
			h.hello(g, msg)
		} else if msg, err := roBox.open(data); err == nil {
			h.mu.Lock()
			g.box, g.readOnly = roBox, true
			h.mu.Unlock()
			h.hello(g, msg)
		} else {
			h.Log("someone tried to join with a wrong key")
			h.kick(id)
		}
		return
	}
	msg, err := g.box.open(data)
	if err != nil || len(msg) == 0 {
		h.kick(id)
		return
	}
	if msg[0] == msgData {
		h.input(g, msg[1:])
	}
}

func (h *Host) hello(g *hostGuest, msg []byte) {
	var hello Hello
	if len(msg) == 0 || msg[0] != msgHello || json.Unmarshal(msg[1:], &hello) != nil {
		h.kick(g.id)
		return
	}
	name := sanitizeName(hello.Name)
	welcome := func() {
		w, _ := json.Marshal(Welcome{Host: h.Name, Session: h.Session, ReadOnly: g.readOnly})
		h.sendMsg(g, msgWelcome, w)
		h.mu.Lock()
		g.name = name
		h.mu.Unlock()
		mode := "can type"
		if g.readOnly {
			mode = "watching"
		}
		h.Log("%s joined (%s)", name, mode)
		h.changed()
	}
	if err := h.startTmux(g, welcome); err != nil {
		h.sendMsg(g, msgBye, []byte("couldn't attach to tmux: "+err.Error()))
		h.kick(g.id)
	}
}

func sanitizeName(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, strings.TrimSpace(s))
	if r := []rune(s); len(r) > 32 {
		s = string(r[:32])
	}
	if s == "" {
		s = "guest"
	}
	return s
}

// startTmux attaches a control-mode client for the guest, calling welcome
// once it's attached.
func (h *Host) startTmux(g *hostGuest, welcome func()) error {
	// A session grouped with the host's shares its windows but has its own
	// current window, so the guest can look around without moving you.
	target := h.Session
	name := fmt.Sprintf("%s-guest-%d-%s", h.Session, g.id, randomID(3))
	if out, err := exec.Command("tmux", append(append([]string(nil), h.TmuxArgs...),
		"new-session", "-d", "-P", "-F", "#{session_id}", "-t", h.Session, "-s", name)...).Output(); err == nil {
		h.tmux("set-option", "-t", name, "@germ-guest", h.Session) // for cleanupGuestSessions
		target, g.session, g.sessionID = name, name, strings.TrimSpace(string(out))
	}
	args := append([]string(nil), h.TmuxArgs...)
	args = append(args, "-C", "attach-session", "-t", target)
	if g.readOnly {
		args = append(args, "-f", "read-only,ignore-size")
	}
	cmd := exec.Command("tmux", args...)
	cmd.Env = os.Environ()
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	g.tmux, g.stdin = cmd, stdin
	go func() {
		buf := make([]byte, 32*1024)
		// Hold output until tmux has attached (it says %session-changed):
		// the guest starts sending commands once welcomed, and commands
		// that arrive earlier run without a session and come back empty.
		var early []byte
		attached := false
		for {
			n, err := stdout.Read(buf)
			if n > 0 && !attached {
				early = append(early, buf[:n]...)
				if bytes.Contains(early, []byte("\n%session-changed ")) || bytes.HasPrefix(early, []byte("%session-changed ")) {
					attached = true
					welcome()
					if h.sendMsg(g, msgData, early) != nil {
						break
					}
					early = nil
				}
			} else if n > 0 {
				if h.sendMsg(g, msgData, buf[:n]) != nil {
					break
				}
			}
			if err != nil {
				break
			}
		}
		cmd.Wait()
		h.mu.Lock()
		still := h.guests[g.id] == g
		h.mu.Unlock()
		if still {
			h.sendMsg(g, msgBye, []byte("the tmux session ended"))
			h.kick(g.id)
		}
	}()
	return nil
}

// cleanupGuestSessions removes guest sessions left over from a share that
// didn't exit cleanly (nobody attached, marked @germ-guest).
func (h *Host) cleanupGuestSessions() {
	out, err := exec.Command("tmux", append(append([]string(nil), h.TmuxArgs...),
		"list-sessions", "-F", "#{session_name}\t#{session_attached}\t#{@germ-guest}")...).Output()
	if err != nil {
		return
	}
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.Split(l, "\t")
		if len(f) == 3 && f[1] == "0" && f[2] == h.Session {
			h.tmux("kill-session", "-t", f[0])
		}
	}
}

// tmux runs a tmux command on the host's server.
func (h *Host) tmux(args ...string) error {
	return exec.Command("tmux", append(append([]string(nil), h.TmuxArgs...), args...)...).Run()
}

// input passes a guest's tmux commands through the filter.
func (h *Host) input(g *hostGuest, data []byte) {
	if g.stdin == nil {
		return
	}
	g.pending = append(g.pending, data...)
	if len(g.pending) > 1<<20 {
		h.kick(g.id) // no sane command is this long
		return
	}
	for {
		i := strings.IndexByte(string(g.pending), '\n')
		if i < 0 {
			return
		}
		line := string(g.pending[:i])
		g.pending = g.pending[i+1:]
		out, ok := filterCommand(line, g.readOnly, g.sessionID)
		if ok && strings.HasPrefix(out, "send-keys") {
			h.noteTyping(g)
		}
		if !ok {
			g.blocked++
			if time.Since(g.lastBlockLog) > 10*time.Second {
				what := truncateLog(line)
				if strings.HasPrefix(line, "send-keys") {
					what = "typing"
				}
				if g.blocked > 1 {
					h.Log("blocked %d actions from %s (latest: %s)", g.blocked, g.name, what)
				} else {
					h.Log("blocked from %s: %s", g.name, what)
				}
				g.lastBlockLog, g.blocked = time.Now(), 0
			}
		}
		if _, err := io.WriteString(g.stdin, out+"\n"); err != nil {
			return
		}
	}
}

func truncateLog(s string) string {
	if len(s) > 60 {
		return s[:59] + "…"
	}
	return s
}

// noteTyping marks a guest as typing; typingLoop clears it.
func (h *Host) noteTyping(g *hostGuest) {
	h.mu.Lock()
	g.typed = time.Now()
	started := !g.typing
	g.typing = true
	h.mu.Unlock()
	if started {
		h.changed()
	}
}

// typingLoop clears "typing" a few seconds after a guest's last key.
func (h *Host) typingLoop(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		h.mu.Lock()
		changed := false
		for _, g := range h.guests {
			if g.typing && time.Since(g.typed) > 3*time.Second {
				g.typing, changed = false, true
			}
		}
		h.mu.Unlock()
		if changed {
			h.changed()
		}
	}
}

func (h *Host) stopGuest(g *hostGuest) {
	if g.session != "" {
		go h.tmux("kill-session", "-t", g.session)
	}
	if g.stdin != nil {
		io.WriteString(g.stdin, "detach-client\n")
		g.stdin.Close()
	}
	if g.tmux != nil && g.tmux.Process != nil {
		p := g.tmux.Process
		go func() {
			time.Sleep(2 * time.Second)
			p.Kill()
		}()
	}
}

// Participants lists who is in the session, host first.
func (h *Host) Participants() []Participant {
	h.mu.Lock()
	defer h.mu.Unlock()
	var gs []*hostGuest
	for _, g := range h.guests {
		if g.name != "" {
			gs = append(gs, g)
		}
	}
	sort.Slice(gs, func(i, j int) bool { return gs[i].joined.Before(gs[j].joined) })
	out := []Participant{{Name: h.Name, Host: true}}
	for _, g := range gs {
		out = append(out, Participant{ID: g.id, Name: g.name, ReadOnly: g.readOnly, Typing: g.typing})
	}
	return out
}

// changed tells everyone who's here.
func (h *Host) changed() {
	ps := h.Participants()
	body, _ := json.Marshal(ps)
	h.mu.Lock()
	var gs []*hostGuest
	for _, g := range h.guests {
		if g.box != nil && g.name != "" {
			gs = append(gs, g)
		}
	}
	h.mu.Unlock()
	for _, g := range gs {
		h.sendMsg(g, msgPresence, body)
	}
	if h.OnChange != nil {
		h.OnChange(ps)
	}
}

// Remove disconnects a guest by ID.
func (h *Host) Remove(id uint32) bool {
	h.mu.Lock()
	g := h.guests[id]
	h.mu.Unlock()
	if g == nil {
		return false
	}
	h.sendMsg(g, msgBye, []byte("the host removed you from the session"))
	h.kick(id)
	return true
}

// RotateLinks makes new invite links. People already in the session stay;
// the old links (and anyone holding them, like a removed guest) stop
// working.
func (h *Host) RotateLinks() error {
	rw, err := NewKey()
	if err != nil {
		return err
	}
	ro, err := NewKey()
	if err != nil {
		return err
	}
	h.mu.Lock()
	h.rwKey, h.roKey, h.rw, h.ro = rw, ro, newBox(rw), newBox(ro)
	h.mu.Unlock()
	return nil
}

// Kick removes a guest by name.
func (h *Host) Kick(name string) bool {
	h.mu.Lock()
	var ids []uint32
	for id, g := range h.guests {
		if g.name == name {
			ids = append(ids, id)
		}
	}
	h.mu.Unlock()
	for _, id := range ids {
		h.kick(id)
	}
	return len(ids) > 0
}

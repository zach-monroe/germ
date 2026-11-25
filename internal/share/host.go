package share

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/zach-monroe/germ/internal/pty"
)

// Host shares a tmux session: each guest gets its own tmux client (in a
// session grouped with the host's), tunnelled through the relay. Everyone
// can watch and chat; typing in a tab takes its helm (see room.go).
type Host struct {
	Relay *url.URL // public base URL, used in links
	// Dial, if set, is where the host itself connects (germ share's
	// built-in relay, on this machine); default Relay.
	Dial     *url.URL
	Session  string   // tmux session to share
	TmuxArgs []string // e.g. ["-L", "socket"]; empty uses $TMUX / default
	Name     string   // shown to guests, e.g. zach@laptop
	// Protect is a tmux pane guests never type into, even with the helm
	// of its tab: the share pane, where the host chats and decides.
	// Typing aimed at it goes to the tab's other pane instead.
	Protect string
	// OnChange is called (from a background goroutine) when people come
	// and go, or start and stop typing.
	OnChange func([]Participant)
	// OnChat is called with each new chat message.
	OnChat func(ChatMsg)
	// OnState is called when tabs, the helm or requests change (as the
	// host sees them).
	OnState func(State)
	// OnHelmRequest is called when someone asks the host for the helm;
	// answer with Act(Action{Op: OpDecide, ...}).
	OnHelmRequest func(HelmRequest)
	// Log receives notable events.
	Log func(format string, args ...any)
	// Approval says whose joins the host approves: "all" (default) or
	// "none".
	Approval string
	// OnRequest is called (in the background) when a guest waits to join;
	// answer with Decide. Without it, nobody needing approval gets in.
	OnRequest func(JoinRequest)
	// HostToken, if set, is sent to the relay to prove this is the host
	// (germ share's built-in relay requires it).
	HostToken string

	Room     string
	key      Key
	box      *box
	conn     *websocket.Conn
	writeMu  sync.Mutex
	mu       sync.Mutex
	guests   map[uint32]*hostGuest // by relay connection
	approved map[string]bool       // client IDs let in before
	waiting  map[uint32]JoinRequest

	// The room (room.go).
	people        map[uint32]*person
	nextPerson    uint32
	chat          []ChatMsg
	seq           uint64
	hostComposing time.Time
	tabs          map[string]*tabState // by window ID
	tabOrder      []string
	current       string            // the host's window
	paneWin       map[string]string // pane ID -> window ID
	requests      []*helmReq
	nextReq       uint32
	newWindowBy   uint32 // who last opened a window, and when
	newWindowAt   time.Time
	refreshMu     sync.Mutex
	refreshed     time.Time
}

type hostGuest struct {
	id        uint32
	person    *person
	name      string
	tmux      *exec.Cmd
	stdin     io.WriteCloser
	pending   []byte // partial command line
	session   string // the guest's own session, grouped with the host's
	sessionID string // its ID, e.g. "$4"
	typed     time.Time
	typing    bool
	web       bool   // browser guest: a tmux attach in a pty
	client    string // guest's random ID (Hello.Client)
	hello     Hello
	opened    bool // said a valid hello
	pty       *pty.Session
	// Browser guests: the window they look at (and its active pane, cached
	// briefly), and whether it follows the host's.
	viewing    string
	follow     bool
	curPane    string
	curAt      time.Time
	lastNoHelm time.Time
	// Blocked commands are logged at most every few seconds.
	lastBlockLog time.Time
	blocked      int
}

// NewHost prepares a share with a fresh room and key.
func NewHost(relay *url.URL, session string) (*Host, error) {
	k, err := NewKey()
	if err != nil {
		return nil, err
	}
	return &Host{Relay: relay, Session: session, Room: randomID(12), key: k, box: newBox(k),
		guests: map[uint32]*hostGuest{}, approved: map[string]bool{}, waiting: map[uint32]JoinRequest{},
		people: map[uint32]*person{}, tabs: map[string]*tabState{}, paneWin: map[string]string{},
		Log: func(string, ...any) {}}, nil
}

// Link returns the invite link.
func (h *Host) Link() Link {
	h.mu.Lock()
	defer h.mu.Unlock()
	return Link{Relay: h.Relay, Room: h.Room, Key: h.key}
}

// Run connects to the relay and serves guests until ctx ends or the
// connection drops.
func (h *Host) Run(ctx context.Context) error {
	link := h.Link()
	if h.Dial != nil {
		link.Relay = h.Dial
	}
	var opts *websocket.DialOptions
	if h.HostToken != "" {
		opts = &websocket.DialOptions{HTTPHeader: http.Header{hostTokenHeader: {h.HostToken}}}
	}
	c, _, err := websocket.Dial(ctx, link.websocketURL("host"), opts)
	if err != nil {
		return fmt.Errorf("connecting to relay %s: %w", h.Relay.Host, err)
	}
	c.SetReadLimit(maxMessage + 16)
	h.conn = c
	h.cleanupGuestSessions()
	h.refreshTabs()
	go h.tickLoop(ctx)
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
		for _, g := range h.admitted() {
			h.sendMsg(g, msgBye, []byte("the host stopped sharing"))
		}
		c.Close(websocket.StatusNormalClosure, "host stopped sharing")
	}()
	defer func() {
		h.mu.Lock()
		for _, g := range h.guests {
			h.stopGuest(g)
			if g.person != nil && g.person.conn == g {
				g.person.conn, g.person.goneAt = nil, time.Now()
			}
		}
		h.guests = map[uint32]*hostGuest{}
		h.waiting = map[uint32]JoinRequest{}
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
			h.guests[id] = &hostGuest{id: id}
			h.mu.Unlock()
		case kindLeave:
			h.leave(id)
		case kindData:
			h.fromGuest(id, data[5:])
		}
	}
}

// leave forgets a connection; the person's tabs wait for them a while.
func (h *Host) leave(id uint32) {
	h.mu.Lock()
	g := h.guests[id]
	delete(h.guests, id)
	delete(h.waiting, id)
	var p *person
	if g != nil {
		h.stopGuest(g)
		if g.person != nil && g.person.conn == g {
			p = g.person
			p.conn, p.goneAt, p.composing = nil, time.Now(), time.Time{}
		}
	}
	h.mu.Unlock()
	if p != nil {
		h.Log("%s left", ShortName(p.name))
		h.deliver(&effects{presence: true, state: true})
	}
}

// admitted lists guests who are in (said hello and were let in).
func (h *Host) admitted() []*hostGuest {
	h.mu.Lock()
	defer h.mu.Unlock()
	var gs []*hostGuest
	for _, g := range h.guests {
		if g.person != nil {
			gs = append(gs, g)
		}
	}
	return gs
}

func (h *Host) send(id uint32, kind byte, payload []byte) error {
	h.writeMu.Lock()
	defer h.writeMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return h.conn.Write(ctx, websocket.MessageBinary, frame(kind, id, payload))
}

func (h *Host) sendMsg(g *hostGuest, typ byte, body []byte) error {
	return h.send(g.id, kindData, h.box.seal(append([]byte{typ}, body...)))
}

func (h *Host) sendJSON(g *hostGuest, typ byte, v any) error {
	b, _ := json.Marshal(v)
	return h.sendMsg(g, typ, b)
}

func (h *Host) kick(id uint32) { h.send(id, kindKick, nil) }

func (h *Host) fromGuest(id uint32, data []byte) {
	h.mu.Lock()
	g := h.guests[id]
	b := h.box
	h.mu.Unlock()
	if g == nil {
		return
	}
	msg, err := b.open(data)
	if err != nil || len(msg) == 0 {
		if !g.opened {
			h.Log("someone tried to join with a wrong key")
		}
		h.kick(id)
		return
	}
	if !g.opened {
		g.opened = true
		h.hello(g, msg)
		return
	}
	h.mu.Lock()
	in := g.person != nil
	h.mu.Unlock()
	if !in {
		return // still waiting to be let in
	}
	switch msg[0] {
	case msgData:
		if g.web {
			h.webInput(g, msg[1:])
		} else {
			h.input(g, msg[1:])
		}
	case msgResize:
		var sz Size
		if g.web && g.pty != nil && json.Unmarshal(msg[1:], &sz) == nil && sz.Cols > 0 && sz.Rows > 0 {
			g.pty.Resize(uint16(min(sz.Cols, 500)), uint16(min(sz.Rows, 300)))
		}
	case msgAction:
		var a Action
		if json.Unmarshal(msg[1:], &a) == nil {
			h.guestAction(g, a)
		}
	}
}

func (h *Host) hello(g *hostGuest, msg []byte) {
	var hello Hello
	if msg[0] != msgHello || json.Unmarshal(msg[1:], &hello) != nil {
		h.kick(g.id)
		return
	}
	name := sanitizeName(hello.Name)
	if hello.Kind == "web" { // older viewers added it to the name; Participant.Web says it now
		name = sanitizeName(strings.TrimSuffix(name, " (browser)"))
	}
	if hello.Version != protocolVersion {
		h.sendMsg(g, msgBye, []byte("this session needs the same germ version as the host's (update germ and try again)"))
		h.kick(g.id)
		return
	}
	h.mu.Lock()
	g.client, g.hello, g.web = hello.Client, hello, hello.Kind == "web"
	ask := h.Approval != "none" && !(g.client != "" && h.approved[g.client])
	tooMany := len(h.waiting) >= 5
	if ask && !tooMany && h.OnRequest != nil {
		h.waiting[g.id] = JoinRequest{ID: g.id, Name: name, Web: g.web}
	}
	h.mu.Unlock()
	if ask {
		if tooMany || h.OnRequest == nil {
			h.sendMsg(g, msgBye, []byte("the host isn't taking more guests right now"))
			h.kick(g.id)
			return
		}
		h.sendMsg(g, msgWaiting, nil)
		go h.OnRequest(JoinRequest{ID: g.id, Name: name, Web: g.web})
		go func() { // don't keep people waiting forever
			time.Sleep(2 * time.Minute)
			if h.Decide(g.id, false) {
				h.Log("%s waited too long and was turned away", name)
			}
		}()
		return
	}
	h.admit(g, name)
}

// JoinRequests lists guests waiting to be let in.
func (h *Host) JoinRequests() []JoinRequest {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []JoinRequest
	for _, r := range h.waiting {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Decide answers a JoinRequest. It reports false if the guest is no longer
// waiting (they left, or it was already decided).
func (h *Host) Decide(id uint32, allow bool) bool {
	h.mu.Lock()
	g := h.guests[id]
	_, waiting := h.waiting[id]
	if g == nil || !waiting {
		h.mu.Unlock()
		return false
	}
	delete(h.waiting, id)
	if allow && g.client != "" {
		h.approved[g.client] = true // reconnects get straight back in
	}
	name := sanitizeName(g.hello.Name)
	h.mu.Unlock()
	if !allow {
		h.sendMsg(g, msgBye, []byte("the host didn't let you in"))
		h.kick(id)
		return true
	}
	h.admit(g, name)
	return true
}

// admit starts a guest's view of the session.
func (h *Host) admit(g *hostGuest, name string) {
	welcome := func() {
		h.mu.Lock()
		p, isNew := h.personFor(g, name)
		g.person, g.name = p, p.name
		back := p.announced
		p.announced = false
		e := &effects{presence: true}
		if back {
			h.event(e, "%s is back", ShortName(p.name))
		} else if isNew {
			h.event(e, "%s joined", ShortName(p.name))
		}
		history := h.recentChat()
		h.mu.Unlock()
		h.sendJSON(g, msgWelcome, Welcome{Host: h.Name, Session: h.Session, You: p.id})
		h.sendJSON(g, msgHistory, history)
		e.state = true
		h.deliver(e)
	}
	var err error
	if g.web {
		err = h.startWebTmux(g, g.hello.Size, welcome)
	} else {
		err = h.startTmux(g, welcome)
	}
	if err != nil {
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

// groupedSession makes the guest's own session, grouped with the host's:
// it shares the windows but has its own current window, so the guest can
// look around without moving the host.
func (h *Host) groupedSession(g *hostGuest) string {
	name := fmt.Sprintf("%s-guest-%d-%s", h.Session, g.id, randomID(3))
	out, err := exec.Command("tmux", append(append([]string(nil), h.TmuxArgs...),
		"new-session", "-d", "-P", "-F", "#{session_id}", "-t", h.Session, "-s", name)...).Output()
	if err != nil {
		return h.Session
	}
	h.tmux("set-option", "-t", name, "@germ-guest", h.Session) // for cleanupGuestSessions
	g.session, g.sessionID = name, strings.TrimSpace(string(out))
	return name
}

// startTmux attaches a control-mode client for the guest, calling welcome
// once it's attached. Guests never resize the host's windows.
func (h *Host) startTmux(g *hostGuest, welcome func()) error {
	target := h.groupedSession(g)
	args := append([]string(nil), h.TmuxArgs...)
	args = append(args, "-C", "attach-session", "-f", "ignore-size", "-t", target)
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

func (h *Host) tmuxOutput(args ...string) string {
	out, _ := exec.Command("tmux", append(append([]string(nil), h.TmuxArgs...), args...)...).Output()
	return strings.TrimSpace(string(out))
}

// input passes a germ guest's tmux commands through the filter and the
// helm.
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
		i := bytes.IndexByte(g.pending, '\n')
		if i < 0 {
			return
		}
		line := string(g.pending[:i])
		g.pending = g.pending[i+1:]
		out := strings.TrimSpace(line)
		class, target := filterCommand(line, g.sessionID)
		switch class {
		case cmdHelm, cmdNewWindow:
			w := h.targetWindow(g, target)
			if !h.hasHelm(g, w) {
				h.noHelm(g, w)
				out = blockedCommand
				break
			}
			if class == cmdNewWindow {
				h.mu.Lock()
				h.newWindowBy, h.newWindowAt = g.person.id, time.Now()
				h.mu.Unlock()
			}
			if h.Protect != "" && class == cmdHelm {
				if fixed, ok := h.protect(g, out, target); ok {
					out = fixed
				} else {
					out = blockedCommand
					break
				}
			}
			if strings.HasPrefix(out, "send") {
				h.noteTyping(g)
			}
		case cmdBlocked:
			out = blockedCommand
			g.blocked++
			if time.Since(g.lastBlockLog) > 10*time.Second {
				if g.blocked > 1 {
					h.Log("blocked %d actions from %s (latest: %s)", g.blocked, g.name, truncateLog(line))
				} else {
					h.Log("blocked from %s: %s", g.name, truncateLog(line))
				}
				g.lastBlockLog, g.blocked = time.Now(), 0
			}
		}
		if _, err := io.WriteString(g.stdin, out+"\n"); err != nil {
			return
		}
	}
}

// protect keeps a guest's command off the protected pane: typing there
// goes to the tab's other pane, and anything else that targets it is
// refused (ok false).
func (h *Host) protect(g *hostGuest, line, target string) (string, bool) {
	pane := target
	if !strings.HasPrefix(target, "%") {
		t := target
		if t == "" {
			t = g.sessionID
		}
		pane = h.tmuxOutput("display", "-p", "-t", t, "#{pane_id}")
	}
	if pane != h.Protect {
		return line, true
	}
	name, rest, _ := strings.Cut(line, " ")
	if name != "send-keys" && name != "send" {
		return "", false
	}
	other := h.otherPane(h.Protect)
	if other == "" {
		return "", false
	}
	if target != "" && strings.Contains(rest, target) {
		return name + " " + strings.Replace(rest, target, other, 1), true
	}
	return name + " -t " + other + " " + rest, true
}

// otherPane is the pane in the same tab as pane that was used last ("" if
// it's alone).
func (h *Host) otherPane(pane string) string {
	out := h.tmuxOutput("list-panes", "-t", pane, "-F", "#{pane_id} #{pane_last}")
	first := ""
	for _, l := range strings.Split(out, "\n") {
		id, last, _ := strings.Cut(strings.TrimSpace(l), " ")
		if id == "" || id == pane {
			continue
		}
		if last == "1" {
			return id
		}
		if first == "" {
			first = id
		}
	}
	return first
}

// targetWindow resolves a command's -t target to a window ID ("" if
// unknown). An empty target is the guest's current pane.
func (h *Host) targetWindow(g *hostGuest, target string) string {
	if _, w, ok := strings.Cut(target, ":"); ok {
		target = w
		if i := strings.IndexByte(target, '.'); i >= 0 {
			target = target[:i]
		}
	}
	switch {
	case target == "":
		if g.sessionID == "" {
			return ""
		}
		return h.tmuxOutput("display", "-p", "-t", g.sessionID, "#{window_id}")
	case strings.HasPrefix(target, "@"):
		return target
	case strings.HasPrefix(target, "%"):
		h.mu.Lock()
		w := h.paneWin[target]
		h.mu.Unlock()
		if w == "" {
			h.refreshTabs() // a new pane
			h.mu.Lock()
			w = h.paneWin[target]
			h.mu.Unlock()
		}
		return w
	}
	return ""
}

// hasHelm says whether the guest may type in window.
func (h *Host) hasHelm(g *hostGuest, window string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	t := h.tabs[window]
	return t != nil && g.person != nil && t.Helm == g.person.id
}

// noHelm tells a guest (now and then) why their typing went nowhere.
func (h *Host) noHelm(g *hostGuest, window string) {
	if time.Since(g.lastNoHelm) < 5*time.Second {
		return
	}
	g.lastNoHelm = time.Now()
	h.mu.Lock()
	who := "the host"
	if t := h.tabs[window]; t != nil {
		who = h.nameOf(t.Helm)
	}
	h.mu.Unlock()
	h.sendNotice(g, fmt.Sprintf("%s has the helm here; ask for it to type", who))
}

func (h *Host) sendNotice(g *hostGuest, text string) {
	h.sendJSON(g, msgChat, ChatMsg{Event: true, Text: text, At: time.Now().UnixMilli()})
}

func truncateLog(s string) string {
	if len(s) > 60 {
		return s[:59] + "…"
	}
	return s
}

// noteTyping marks a guest as typing; tickLoop clears it.
func (h *Host) noteTyping(g *hostGuest) {
	h.mu.Lock()
	g.typed = time.Now()
	started := !g.typing
	g.typing = true
	h.mu.Unlock()
	if started {
		h.deliver(&effects{presence: true})
	}
}

// tickLoop keeps the room current: tabs from tmux, typing, requests and
// people who left.
func (h *Host) tickLoop(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		e := &effects{}
		if h.refreshTabs() {
			e.state = true
		}
		h.mu.Lock()
		for _, g := range h.guests {
			if g.typing && time.Since(g.typed) > 3*time.Second {
				g.typing, e.presence = false, true
			}
		}
		h.expire(e)
		h.mu.Unlock()
		h.deliver(e)
	}
}

// refreshTabs reads the session's windows and panes from tmux, and moves
// browser guests who follow the host. It reports whether the tabs changed.
func (h *Host) refreshTabs() bool {
	h.refreshMu.Lock()
	defer h.refreshMu.Unlock()
	out := h.tmuxOutput("list-windows", "-t", h.Session, "-F", "W\t#{window_id}\t#{window_index}\t#{window_name}\t#{window_active}",
		";", "list-panes", "-s", "-t", h.Session, "-F", "P\t#{pane_id}\t#{window_id}",
		";", "list-sessions", "-F", "S\t#{session_id}\t#{window_id}")
	if out == "" {
		return false
	}
	var order []string
	panes := map[string]string{}
	sessWin := map[string]string{}
	type win struct {
		index int
		name  string
	}
	wins := map[string]win{}
	current := ""
	for _, l := range strings.Split(out, "\n") {
		f := strings.Split(l, "\t")
		switch {
		case len(f) == 5 && f[0] == "W":
			var idx int
			fmt.Sscan(f[2], &idx)
			order = append(order, f[1])
			wins[f[1]] = win{idx, f[3]}
			if f[4] == "1" {
				current = f[1]
			}
		case len(f) == 3 && f[0] == "P":
			panes[f[1]] = f[2]
		case len(f) == 3 && f[0] == "S":
			sessWin[f[1]] = f[2]
		}
	}
	if len(order) == 0 {
		return false
	}
	h.mu.Lock()
	changed := current != h.current || strings.Join(order, ",") != strings.Join(h.tabOrder, ",")
	for _, t := range h.tabs {
		t.seen = false
	}
	for _, id := range order {
		w := wins[id]
		t := h.tabs[id]
		if t == nil {
			t = &tabState{Tab: Tab{Window: id}}
			// A window a guest just opened is theirs to type in.
			if h.newWindowBy != 0 && time.Since(h.newWindowAt) < 3*time.Second {
				t.Helm = h.newWindowBy
				h.newWindowBy = 0
			}
			h.tabs[id] = t
		}
		if t.Index != w.index || t.Name != w.name {
			t.Index, t.Name, changed = w.index, w.name, true
		}
		t.seen = true
	}
	for id, t := range h.tabs {
		if !t.seen {
			delete(h.tabs, id)
			for _, r := range append([]*helmReq(nil), h.requests...) {
				if r.Window == id {
					h.dropRequest(r.ID)
				}
			}
		}
	}
	h.tabOrder, h.current, h.paneWin, h.refreshed = order, current, panes, time.Now()
	type move struct{ sess, win string }
	var moves []move
	for _, g := range h.guests {
		if !g.web || g.sessionID == "" || g.person == nil {
			continue
		}
		if g.follow && current != "" && sessWin[g.sessionID] != current {
			moves = append(moves, move{g.sessionID, current})
			sessWin[g.sessionID] = current
		}
		if v := sessWin[g.sessionID]; v != "" && v != g.viewing {
			g.viewing, g.curAt, changed = v, time.Time{}, true
		}
	}
	h.mu.Unlock()
	for _, m := range moves {
		h.tmux("select-window", "-t", m.sess+":"+m.win)
	}
	return changed
}

// guestAction applies something a guest did.
func (h *Host) guestAction(g *hostGuest, a Action) {
	switch a.Op {
	case OpView, OpFollow:
		if !g.web || g.sessionID == "" {
			return
		}
		h.mu.Lock()
		win := a.Window
		if a.Op == OpFollow {
			g.follow, win = true, h.current
		} else if h.tabs[win] != nil {
			g.follow = false
		} else {
			win = ""
		}
		h.mu.Unlock()
		if win != "" {
			h.tmux("select-window", "-t", g.sessionID+":"+win)
			h.mu.Lock()
			g.viewing, g.curAt = win, time.Time{}
			h.mu.Unlock()
		}
		h.deliver(&effects{state: true})
		return
	case OpSay, OpComposing, OpRequest, OpRelease, OpDecide:
	default:
		return // the host's own
	}
	e := &effects{}
	h.mu.Lock()
	if g.person != nil {
		h.act(e, g.person.id, a)
	}
	h.mu.Unlock()
	h.deliver(e)
}

// Act does something as the host.
func (h *Host) Act(a Action) {
	if a.Op == OpJoin {
		h.Decide(a.Request, a.Allow)
		return
	}
	if a.Op == OpGive {
		h.mu.Lock()
		known := h.tabs[a.Window] != nil
		h.mu.Unlock()
		if !known {
			h.refreshTabs() // a tab opened just now
		}
	}
	e := &effects{}
	h.mu.Lock()
	h.act(e, 0, a)
	h.mu.Unlock()
	h.deliver(e)
}

// Event posts a session event to the chat (e.g. "chat saved").
func (h *Host) Event(format string, args ...any) {
	e := &effects{}
	h.mu.Lock()
	h.event(e, format, args...)
	h.mu.Unlock()
	h.deliver(e)
}

// deliver sends what changed to everyone, and to the host's callbacks.
func (h *Host) deliver(e *effects) {
	if len(e.chat) == 0 && len(e.notices) == 0 && !e.state && !e.presence && len(e.requests) == 0 {
		return
	}
	gs := h.admitted()
	for _, m := range e.chat {
		for _, g := range gs {
			h.sendJSON(g, msgChat, m)
		}
		if h.OnChat != nil {
			h.OnChat(m)
		}
	}
	for id, text := range e.notices {
		for _, g := range gs {
			if g.person.id == id {
				h.sendNotice(g, text)
			}
		}
	}
	if e.state {
		for _, g := range gs {
			h.mu.Lock()
			s := h.stateFor(g.person.id)
			h.mu.Unlock()
			h.sendJSON(g, msgState, s)
		}
		if h.OnState != nil {
			h.OnState(h.State())
		}
	}
	for _, r := range e.requests {
		if r.Approver == 0 && h.OnHelmRequest != nil {
			h.OnHelmRequest(r)
		}
	}
	if e.presence {
		ps := h.Participants()
		for _, g := range gs {
			h.sendJSON(g, msgPresence, ps)
		}
		if h.OnChange != nil {
			h.OnChange(ps)
		}
	}
}

// State is the room as the host sees it.
func (h *Host) State() State {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.stateFor(0)
}

// Chat returns the recent chat, oldest first.
func (h *Host) Chat() []ChatMsg {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]ChatMsg(nil), h.chat...)
}

// SaveChat writes the chat to a file in ChatDir and returns its path.
func (h *Host) SaveChat() (string, error) {
	msgs := h.Chat()
	dir := ChatDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	name := strings.Map(func(r rune) rune {
		if r == '/' || r == 0 {
			return '-'
		}
		return r
	}, h.Session)
	path := filepath.Join(dir, fmt.Sprintf("%s-%s.md", name, time.Now().Format("2006-01-02-1504")))
	if err := os.WriteFile(path, []byte(formatChat(h.Session, msgs)), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func (h *Host) stopGuest(g *hostGuest) {
	if g.pty != nil {
		g.pty.Close()
	}
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
	out := []Participant{{Name: h.Name, Host: true, Composing: !h.hostComposing.IsZero()}}
	var ps []*person
	for _, p := range h.people {
		if p.conn != nil {
			ps = append(ps, p)
		}
	}
	sort.Slice(ps, func(i, j int) bool { return ps[i].id < ps[j].id })
	for _, p := range ps {
		out = append(out, Participant{ID: p.id, Name: p.name, Web: p.web, Typing: p.conn.typing,
			Composing: !p.composing.IsZero(), Muted: p.muted})
	}
	return out
}

// Remove disconnects a person.
func (h *Host) Remove(id uint32) bool {
	h.mu.Lock()
	p := h.people[id]
	var g *hostGuest
	if p != nil {
		g = p.conn
		delete(h.approved, p.client) // they'd be asked again
	}
	h.mu.Unlock()
	if g == nil {
		return false
	}
	h.sendMsg(g, msgBye, []byte("the host removed you from the session"))
	h.kick(g.id)
	return true
}

// RotateLinks makes a new invite link. People already in the session
// stay; the old link (and anyone holding it, like a removed guest) stops
// working.
func (h *Host) RotateLinks() error {
	k, err := NewKey()
	if err != nil {
		return err
	}
	h.mu.Lock()
	h.key, h.box = k, newBox(k)
	h.mu.Unlock()
	return nil
}

// tmuxSocketArgs points tmux at the host's server explicitly: inside a tmux
// pane $TMUX says which server, but a plain attach refuses to run with
// $TMUX set, so the web attach runs without it.
func (h *Host) tmuxSocketArgs() []string {
	if len(h.TmuxArgs) > 0 {
		return h.TmuxArgs
	}
	if t := os.Getenv("TMUX"); t != "" {
		if sock, _, _ := strings.Cut(t, ","); sock != "" {
			return []string{"-S", sock}
		}
	}
	return nil
}

// startWebTmux attaches a tmux client in a pty for a browser
// guest, to their own grouped session (following the host's window to
// start); what tmux draws streams to the guest's xterm.js. Their
// keystrokes never reach this client: webInput types them into the pane.
func (h *Host) startWebTmux(g *hostGuest, sz Size, welcome func()) error {
	target := h.groupedSession(g)
	g.follow = true
	h.mu.Lock()
	cur := h.current
	h.mu.Unlock()
	if cur != "" && g.sessionID != "" {
		h.tmux("select-window", "-t", g.sessionID+":"+cur)
		g.viewing = cur
	}
	args := []string{"env", "-u", "TMUX", "tmux"}
	args = append(args, h.tmuxSocketArgs()...)
	// Not read-only (-r): tmux runs command-line tmux commands as the most
	// recently active client, and a read-only one refuses them all (the
	// host's included). Nothing is ever written to this pty anyway.
	args = append(args, "attach-session", "-f", "ignore-size", "-t", target)
	cols, rows := max(sz.Cols, 20), max(sz.Rows, 5)
	sess, err := pty.Start(args, uint16(min(cols, 500)), uint16(min(rows, 300)))
	if err != nil {
		return err
	}
	g.pty = sess
	welcome()
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, err := sess.Read(buf)
			if n > 0 && h.sendMsg(g, msgData, buf[:n]) != nil {
				break
			}
			if err != nil {
				break
			}
		}
		sess.Close()
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

// webInput types a browser guest's keystrokes into the pane they're
// looking at, if they have its helm. send-keys skips tmux's key tables,
// so the host's prefix key and command prompt are out of reach.
func (h *Host) webInput(g *hostGuest, data []byte) {
	if g.sessionID == "" || len(data) == 0 {
		return
	}
	win, pane := h.webTarget(g)
	if !h.hasHelm(g, win) {
		h.noHelm(g, win)
		return
	}
	if pane == h.Protect && h.Protect != "" {
		if pane = h.otherPane(pane); pane == "" {
			return
		}
	}
	h.noteTyping(g)
	for len(data) > 0 {
		n := min(len(data), 256)
		args := []string{"send-keys", "-t", pane, "-H"}
		for _, b := range data[:n] {
			args = append(args, fmt.Sprintf("%02x", b))
		}
		h.tmux(args...)
		data = data[n:]
	}
}

// webTarget is the window and active pane a browser guest looks at,
// cached briefly (typing would otherwise ask tmux on every key).
func (h *Host) webTarget(g *hostGuest) (window, pane string) {
	h.mu.Lock()
	if time.Since(g.curAt) < 300*time.Millisecond && g.curPane != "" {
		window, pane = g.viewing, g.curPane
		h.mu.Unlock()
		return
	}
	h.mu.Unlock()
	out := h.tmuxOutput("display", "-p", "-t", g.sessionID, "#{window_id}\t#{pane_id}")
	window, pane, _ = strings.Cut(out, "\t")
	h.mu.Lock()
	g.viewing, g.curPane, g.curAt = window, pane, time.Now()
	h.mu.Unlock()
	return
}

// hostTokenHeader carries Host.HostToken to the relay.
const hostTokenHeader = "X-Germ-Host-Token"

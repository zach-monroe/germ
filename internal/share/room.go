package share

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// The room: who is here (people, stable across reconnects), the chat, and
// each tab's owner and helm. The host is person 0. Everything here is
// guarded by Host.mu; the methods that change something return the
// effects to deliver once the lock is released.

const (
	chatKeep     = 500              // messages kept in memory
	chatHistory  = 200              // sent to someone joining
	maxChatBytes = 4096             // per message
	leaveGrace   = 90 * time.Second // a person's tabs wait this long for them to come back
	requestTTL   = 2 * time.Minute
)

// person is someone who joined, across reconnects.
type person struct {
	id        uint32
	client    string // Hello.Client
	name      string
	web       bool
	conn      *hostGuest // nil while away
	goneAt    time.Time
	muted     bool
	composing time.Time
	announced bool // "left" was posted to the chat
	// Rate limit: a token bucket of messages.
	tokens   float64
	refilled time.Time
}

// tabState is what the host remembers about a tmux window.
type tabState struct {
	Tab
	seen bool // still listed by tmux
}

type helmReq struct {
	HelmRequest
	at time.Time
}

// effects is what to deliver after changing the room.
type effects struct {
	chat     []ChatMsg // broadcast and keep
	notices  map[uint32]string
	state    bool // broadcast State
	presence bool // broadcast Participants
	requests []HelmRequest
}

func (e *effects) notice(p uint32, text string) {
	if e.notices == nil {
		e.notices = map[uint32]string{}
	}
	e.notices[p] = text
}

// personFor finds or creates the person for a guest's client ID, giving
// them a unique name. It reports whether they're new. Caller holds h.mu.
func (h *Host) personFor(g *hostGuest, name string) (*person, bool) {
	if g.client != "" {
		for _, p := range h.people {
			if p.client == g.client {
				if p.conn != nil && p.conn != g {
					p.conn.person = nil // a stale connection (it'll be kicked by the relay)
				}
				p.conn, p.goneAt, p.web = g, time.Time{}, g.web
				return p, false
			}
		}
	}
	h.nextPerson++
	p := &person{id: h.nextPerson, client: g.client, conn: g, web: g.web, tokens: 5, refilled: time.Now()}
	p.name = h.uniqueName(name)
	h.people[p.id] = p
	return p, true
}

// uniqueName returns name, or "name (2)" etc. if someone else here has it.
func (h *Host) uniqueName(name string) string {
	// People see short names ("alice"), so those must differ.
	taken := func(n string) bool {
		if strings.EqualFold(ShortName(n), ShortName(h.Name)) {
			return true
		}
		for _, p := range h.people {
			if strings.EqualFold(ShortName(p.name), ShortName(n)) {
				return true
			}
		}
		return false
	}
	if !taken(name) {
		return name
	}
	for i := 2; ; i++ {
		if n := fmt.Sprintf("%s (%d)", name, i); !taken(n) {
			return n
		}
	}
}

// nameOf is a person's name (the host's for 0). Caller holds h.mu.
func (h *Host) nameOf(id uint32) string {
	if id == 0 {
		return ShortName(h.Name)
	}
	if p := h.people[id]; p != nil {
		return ShortName(p.name)
	}
	return "someone"
}

// ShortName drops the machine from a name: "alice@laptop" -> "alice",
// "alice@laptop (2)" -> "alice (2)".
func ShortName(n string) string {
	if i := strings.IndexByte(n, '@'); i > 0 {
		rest := ""
		if j := strings.Index(n[i:], " ("); j >= 0 {
			rest = n[i+j:]
		}
		n = n[:i] + rest
	}
	return n
}

// tabLabel names a tab in chat events: "tab 2 (zsh)".
func (h *Host) tabLabel(window string) string {
	if t := h.tabs[window]; t != nil {
		if t.Name != "" {
			return fmt.Sprintf("tab %d (%s)", t.Index, t.Name)
		}
		return fmt.Sprintf("tab %d", t.Index)
	}
	return "a tab"
}

// event adds a session event to the chat. Caller holds h.mu.
func (h *Host) event(e *effects, format string, args ...any) {
	e.chat = append(e.chat, h.stamp(ChatMsg{Event: true, Text: fmt.Sprintf(format, args...)}))
}

// stamp numbers a message and keeps it. Caller holds h.mu.
func (h *Host) stamp(m ChatMsg) ChatMsg {
	h.seq++
	m.Seq = h.seq
	m.At = time.Now().UnixMilli()
	h.chat = append(h.chat, m)
	if len(h.chat) > chatKeep {
		h.chat = append([]ChatMsg(nil), h.chat[len(h.chat)-chatKeep:]...)
	}
	return m
}

// cleanChat tidies a chat message: no control characters but newlines,
// no trailing space, at most maxChatBytes.
func cleanChat(s string) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '\n':
			return r
		case r == '\t':
			return ' '
		case r == '\r' || unicode.IsControl(r):
			return -1
		}
		return r
	}, s)
	s = strings.TrimRight(s, " \n")
	s = strings.TrimLeft(s, " \n")
	if len(s) > maxChatBytes {
		s = s[:maxChatBytes]
		for !utf8.ValidString(s) {
			s = s[:len(s)-1]
		}
	}
	return s
}

// say posts a chat message from a person. Caller holds h.mu.
func (h *Host) say(e *effects, from uint32, text string) {
	text = cleanChat(text)
	if text == "" {
		return
	}
	if from != 0 {
		p := h.people[from]
		if p == nil {
			return
		}
		if p.muted {
			e.notice(from, "the host muted you in chat")
			return
		}
		now := time.Now()
		p.tokens = min(5, p.tokens+now.Sub(p.refilled).Seconds()) // one per second, bursts of 5
		p.refilled = now
		if p.tokens < 1 {
			e.notice(from, "slow down: that message wasn't sent")
			return
		}
		p.tokens--
		p.composing = time.Time{}
	} else {
		h.hostComposing = time.Time{}
	}
	e.chat = append(e.chat, h.stamp(ChatMsg{From: from, Name: h.nameOf(from), Host: from == 0, Text: text}))
	e.presence = true // composing stopped
}

// approver says who decides a request for window from someone: the tab's
// owner, if a guest owns it, isn't the one asking, and hasn't had the
// helm taken back by the host; otherwise the host.
func (h *Host) approver(window string, from uint32) uint32 {
	t := h.tabs[window]
	if t == nil || t.Owner == 0 || t.Owner == from || t.Helm == 0 {
		return 0
	}
	if p := h.people[t.Owner]; p == nil || p.conn == nil {
		return 0
	}
	return t.Owner
}

// act applies an Action by person by (0: the host). Caller holds h.mu.
func (h *Host) act(e *effects, by uint32, a Action) {
	isHost := by == 0
	switch a.Op {
	case OpSay:
		h.say(e, by, a.Text)
	case OpComposing:
		if isHost {
			h.hostComposing = time.Now()
		} else if p := h.people[by]; p != nil {
			p.composing = time.Now()
		}
		e.presence = true
	case OpRequest:
		t := h.tabs[a.Window]
		if t == nil || isHost {
			return
		}
		if t.Helm == by {
			e.notice(by, "you already have the helm of "+h.tabLabel(a.Window))
			return
		}
		for _, r := range h.requests {
			if r.From == by && r.Window == a.Window {
				e.notice(by, "you've already asked; waiting for "+h.nameOf(r.Approver))
				return
			}
		}
		h.nextReq++
		r := &helmReq{HelmRequest{ID: h.nextReq, From: by, Name: h.nameOf(by), Window: a.Window,
			Approver: h.approver(a.Window, by)}, time.Now()}
		h.requests = append(h.requests, r)
		h.event(e, "%s asked for the helm of %s", h.nameOf(by), h.tabLabel(a.Window))
		e.requests = append(e.requests, r.HelmRequest)
		e.state = true
	case OpDecide:
		var r *helmReq
		for _, x := range h.requests {
			if x.ID == a.Request {
				r = x
			}
		}
		if r == nil || !(isHost || by == r.Approver) {
			return
		}
		h.dropRequest(r.ID)
		e.state = true
		t := h.tabs[r.Window]
		if t == nil {
			return
		}
		if !a.Allow {
			h.event(e, "%s didn't hand %s the helm of %s", h.nameOf(by), r.Name, h.tabLabel(r.Window))
			return
		}
		t.Helm = r.From
		h.event(e, "%s gave %s the helm of %s", h.nameOf(by), r.Name, h.tabLabel(r.Window))
	case OpRelease:
		t := h.tabs[a.Window]
		if t == nil || isHost || t.Helm != by {
			return
		}
		to := t.Owner
		if to == by {
			to = 0
		}
		t.Helm = to
		h.event(e, "%s handed the helm of %s back to %s", h.nameOf(by), h.tabLabel(a.Window), h.nameOf(to))
		e.state = true
	case OpTake:
		t := h.tabs[a.Window]
		if t == nil || !isHost || t.Helm == 0 {
			return
		}
		from := t.Helm
		t.Helm = 0
		h.event(e, "%s took the helm of %s from %s", h.nameOf(0), h.tabLabel(a.Window), h.nameOf(from))
		e.state = true
	case OpGive:
		t, p := h.tabs[a.Window], h.people[a.Person]
		if t == nil || p == nil || !isHost {
			return
		}
		t.Owner, t.Helm = p.id, p.id
		h.event(e, "%s gave %s to %s", h.nameOf(0), h.tabLabel(a.Window), h.nameOf(p.id))
		e.state = true
	case OpHand:
		t, p := h.tabs[a.Window], h.people[a.Person]
		if t == nil || p == nil || !isHost || t.Helm == p.id {
			return
		}
		t.Helm = p.id
		for _, r := range append([]*helmReq(nil), h.requests...) {
			if r.From == p.id && r.Window == a.Window {
				h.dropRequest(r.ID) // asked for it: answered
			}
		}
		h.event(e, "%s gave %s the helm of %s", h.nameOf(0), h.nameOf(p.id), h.tabLabel(a.Window))
		e.state = true
	case OpUntake:
		t := h.tabs[a.Window]
		if t == nil || !isHost || (t.Owner == 0 && t.Helm == 0) {
			return
		}
		from := t.Owner
		if from == 0 {
			from = t.Helm
		}
		t.Owner, t.Helm = 0, 0
		h.event(e, "%s took %s back from %s", h.nameOf(0), h.tabLabel(a.Window), h.nameOf(from))
		e.state = true
	case OpMute, OpUnmute:
		p := h.people[a.Person]
		if p == nil || !isHost || p.muted == (a.Op == OpMute) {
			return
		}
		p.muted = a.Op == OpMute
		if p.muted {
			h.event(e, "%s muted %s in chat", h.nameOf(0), h.nameOf(p.id))
		} else {
			h.event(e, "%s unmuted %s", h.nameOf(0), h.nameOf(p.id))
		}
		e.presence = true
	}
}

func (h *Host) dropRequest(id uint32) {
	for i, r := range h.requests {
		if r.ID == id {
			h.requests = append(h.requests[:i:i], h.requests[i+1:]...)
			return
		}
	}
}

// expire ends old requests and returns the tabs of people who left for
// good to the host. Caller holds h.mu.
func (h *Host) expire(e *effects) {
	now := time.Now()
	for _, r := range append([]*helmReq(nil), h.requests...) {
		if now.Sub(r.at) > requestTTL {
			h.dropRequest(r.ID)
			e.notice(r.From, "nobody answered your request for the helm of "+h.tabLabel(r.Window))
			e.state = true
		}
	}
	for _, p := range h.people {
		// A blip isn't worth a "left": wait a few seconds for them.
		if p.conn == nil && !p.goneAt.IsZero() && !p.announced && now.Sub(p.goneAt) > 10*time.Second {
			p.announced = true
			h.event(e, "%s left", ShortName(p.name))
		}
		if p.conn != nil || p.goneAt.IsZero() || now.Sub(p.goneAt) < leaveGrace {
			continue
		}
		p.goneAt = time.Time{} // once
		for _, t := range h.tabs {
			if t.Owner == p.id || t.Helm == p.id {
				t.Owner, t.Helm = 0, 0
				h.event(e, "%s went back to %s (%s left)", h.tabLabel(t.Window), h.nameOf(0), h.nameOf(p.id))
				e.state = true
			}
		}
		for _, r := range append([]*helmReq(nil), h.requests...) {
			if r.From == p.id {
				h.dropRequest(r.ID)
				e.state = true
			}
		}
	}
	for _, p := range h.people {
		if !p.composing.IsZero() && now.Sub(p.composing) > 4*time.Second {
			p.composing, e.presence = time.Time{}, true
		}
	}
	if !h.hostComposing.IsZero() && now.Sub(h.hostComposing) > 4*time.Second {
		h.hostComposing, e.presence = time.Time{}, true
	}
}

// stateFor is the room's State as person id sees it. Caller holds h.mu.
func (h *Host) stateFor(id uint32) State {
	s := State{Current: h.current, You: id}
	for _, w := range h.tabOrder {
		if t := h.tabs[w]; t != nil {
			s.Tabs = append(s.Tabs, t.Tab)
		}
	}
	for _, r := range h.requests {
		r.Approver = h.approver(r.Window, r.From) // owners come and go
		s.Requests = append(s.Requests, r.HelmRequest)
	}
	if p := h.people[id]; p != nil && p.conn != nil && p.web {
		s.Viewing, s.Follow = p.conn.viewing, p.conn.follow
	}
	return s
}

// recentChat is what someone joining sees. Caller holds h.mu.
func (h *Host) recentChat() []ChatMsg {
	n := max(0, len(h.chat)-chatHistory)
	return append([]ChatMsg(nil), h.chat[n:]...)
}

// ChatDir is where saved chats go.
func ChatDir() string {
	d := os.Getenv("XDG_DATA_HOME")
	if d == "" {
		home, _ := os.UserHomeDir()
		d = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(d, "germ", "chats")
}

// formatChat renders messages as Markdown-ish text.
func formatChat(session string, msgs []ChatMsg) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# germ chat · %s\n\n", session)
	for _, m := range msgs {
		at := time.UnixMilli(m.At).Format("2006-01-02 15:04")
		if m.Event {
			fmt.Fprintf(&b, "%s  · %s\n", at, m.Text)
			continue
		}
		lines := strings.Split(m.Text, "\n")
		fmt.Fprintf(&b, "%s  %s: %s\n", at, m.Name, lines[0])
		for _, l := range lines[1:] {
			fmt.Fprintf(&b, "%s    %s\n", strings.Repeat(" ", 16), l)
		}
	}
	return b.String()
}

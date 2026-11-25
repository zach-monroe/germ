package app

import (
	"fmt"
	"os"
	"strings"
	"time"
	"unicode"

	"github.com/go-gl/glfw/v3.3/glfw"
	"github.com/mattn/go-runewidth"

	"github.com/zach-monroe/germ/internal/chatview"
	"github.com/zach-monroe/germ/internal/control"
	"github.com/zach-monroe/germ/internal/render"
	"github.com/zach-monroe/germ/internal/share"
	"github.com/zach-monroe/germ/internal/term"
	"github.com/zach-monroe/germ/internal/theme"
)

// The session chat and the helm, germ side. The host's germ hears about
// the session from `germ share` over the control socket (share_update)
// and hands it the host's chat and decisions (share_wait); a guest's germ
// talks to the host directly (share.Guest). Either way the chat is a
// panel docked on the right of the window, drawn as a little terminal.

const (
	chatWidth     = 42               // columns, separator included
	chatStale     = 60 * time.Second // forget a share we stop hearing from
	chatWaitLimit = 8 * time.Second  // share_wait answers empty after this
)

type chatState struct {
	// Where the session comes from.
	host       bool         // germ share updates us
	guest      *share.Guest // we joined someone's session
	session    string
	link       string
	sharePane  string // host: the tmux pane germ share runs in
	lastUpdate time.Time
	sendq      chan share.Action // guest: actions, sent in order

	msgs   []share.ChatMsg
	people []share.Participant
	state  share.State
	joins  []share.JoinRequest
	you    uint32
	names  map[uint32]string // everyone seen, for tabs owned by people away
	seen   map[string]bool   // requests already announced

	open, focused bool
	input         []rune
	cur           int
	sent          []string // sent messages, for Up
	sentAt        int
	scroll        int    // rows scrolled up from the newest
	readSeq       uint64 // newest message read with the panel open
	divider       uint64 // "new" goes before the first message after this
	unread        int
	toast         string
	toastUntil    time.Time
	lastComposing time.Time
	quiet         bool // adding history: no toasts or unread counts

	// Host: what the host did, waiting for germ share to collect it.
	pending []share.Action
	waiter  func(*control.Response)
	waitID  int

	term     *term.Terminal
	surf     *render.Surface
	theme    *theme.Theme
	dirty    bool
	rowLinks []string // plain text of each drawn row, for clicks
}

func newChatState(s *render.Surface) *chatState {
	return &chatState{surf: s, names: map[uint32]string{}, seen: map[string]bool{}}
}

// active reports whether we're in a shared session with a chat.
func (c *chatState) active() bool {
	return c.guest != nil || (c.host && time.Since(c.lastUpdate) < chatStale)
}

// visible reports whether the panel is on screen.
func (c *chatState) visible() bool { return c.open && c.active() }

// chatCols is how many grid columns the panel takes.
func (a *App) chatCols() int {
	if !a.chat.visible() || a.gridCols < 60 {
		return 0
	}
	return min(chatWidth, a.gridCols/2)
}

// chatLayout resizes panes after the panel opened or closed.
func (a *App) chatLayout() {
	if a.tmux != nil {
		a.tmux.clientSize(a.content())
	}
	a.relayout()
	a.chat.dirty = true
	a.forceRedraw = true
}

// toggleChat opens and focuses the panel; when it's focused, closes it.
func (a *App) toggleChat() bool {
	c := a.chat
	if !c.active() {
		a.chatToast("chat is for shared sessions: Ctrl+Shift+P shares this one", 4*time.Second)
		return true
	}
	if p := a.sharePane(); p != nil { // the host chats in the share pane
		if c.open {
			c.open, c.focused = false, false
			a.chatLayout()
		}
		a.focusPane(p)
		a.markRead()
		return true
	}
	switch {
	case !c.open:
		c.open, c.focused = true, true
		a.chatLayout()
		a.markRead()
	case !c.focused:
		c.focused = true
		a.markRead()
	default:
		c.open, c.focused = false, false
		a.chatLayout()
	}
	c.dirty, a.forceRedraw = true, true
	return true
}

// markRead clears the unread count, leaving a "new" divider where
// reading starts.
func (a *App) markRead() {
	c := a.chat
	if c.unread > 0 {
		c.divider = c.readSeq
	} else {
		c.divider = 0
	}
	c.unread, c.scroll = 0, 0
	if n := len(c.msgs); n > 0 {
		c.readSeq = c.msgs[n-1].Seq
	}
	c.dirty = true
}

// reading reports whether the user is looking at the chat right now.
func (a *App) reading() bool {
	return a.focused && (a.chat.visible() && a.chat.scroll == 0 || a.inSharePane())
}

// sharePane is the pane germ share runs in, when this window is the
// host's and shows it.
func (a *App) sharePane() *pane {
	c := a.chat
	if !c.host || !c.active() || c.sharePane == "" {
		return nil
	}
	p, err := a.resolvePane(c.sharePane)
	if err != nil {
		return nil
	}
	return p
}

// inSharePane reports whether the share pane is the pane being used.
func (a *App) inSharePane() bool {
	p := a.sharePane()
	return p != nil && a.active() == p
}

// ---- updates -------------------------------------------------------------

// shareUpdate is germ share telling the host's window about the session.
func (a *App) shareUpdate(u *share.Update) (uint64, error) {
	c := a.chat
	if a.tmux == nil || a.tmux.remote != nil || u.Session != a.tmux.session {
		return 0, fmt.Errorf("this window isn't showing session %q", u.Session)
	}
	if u.Ended {
		a.endChat()
		return 0, nil
	}
	fresh := !c.active()
	if fresh {
		c.msgs, c.seen, c.unread, c.readSeq = nil, map[string]bool{}, 0, 0
	}
	c.host, c.guest, c.session, c.link, c.lastUpdate, c.you = true, nil, u.Session, u.Link, time.Now(), 0
	c.sharePane = u.Pane
	c.joins = u.Joins
	a.chatPeople(u.People)
	a.chatState(u.State)
	c.quiet = fresh && len(u.Chat) > 1 // a window opened mid-share catches up quietly
	for _, m := range u.Chat {
		a.chatMessage(m)
	}
	c.quiet = false
	for _, j := range u.Joins {
		if k := fmt.Sprintf("join/%d", j.ID); !c.seen[k] {
			c.seen[k] = true
			where := ""
			if j.Web {
				where = " (browser)"
			}
			a.chatAlert(fmt.Sprintf("%s%s wants to join", j.Name, where))
		}
	}
	if u.Notice != "" {
		a.chatToast(u.Notice, 6*time.Second)
	}
	return a.lastSeq(), nil
}

func (a *App) lastSeq() uint64 {
	if n := len(a.chat.msgs); n > 0 {
		return a.chat.msgs[n-1].Seq
	}
	return 0
}

// shareWait hands germ share what the host did, waiting a while for
// something to happen.
func (a *App) shareWait(reply func(*control.Response)) {
	c := a.chat
	if c.waiter != nil {
		c.waiter(&control.Response{OK: true}) // a newer waiter replaces it
		c.waiter = nil
	}
	if len(c.pending) > 0 {
		reply(&control.Response{OK: true, Actions: c.pending})
		c.pending = nil
		return
	}
	c.waitID++
	id := c.waitID
	c.waiter = reply
	time.AfterFunc(chatWaitLimit, func() {
		a.mainThread(func() {
			if c.waitID == id && c.waiter != nil {
				c.waiter(&control.Response{OK: true})
				c.waiter = nil
			}
		})
	})
}

// endChat forgets the shared session (it stopped).
func (a *App) endChat() {
	c := a.chat
	wasOpen := c.visible()
	c.host, c.guest, c.open, c.focused = false, nil, false, false
	c.people, c.state, c.joins, c.unread = nil, share.State{}, nil, 0
	if wasOpen {
		a.chatLayout()
	}
	a.forceRedraw = true
}

// startGuestChat wires a joined session's chat to the panel.
func (a *App) startGuestChat(g *share.Guest) {
	c := a.chat
	c.guest, c.host, c.you = g, false, g.Welcome.You
	c.session = g.Welcome.Session
	c.sendq = make(chan share.Action, 64)
	go func(q chan share.Action) {
		for act := range q {
			g.Act(act)
		}
	}(c.sendq)
	g.OnChat = func(m share.ChatMsg) { a.mainThread(func() { a.chatMessage(m) }) }
	g.OnHistory = func(ms []share.ChatMsg) { // after a reconnect: what we missed
		a.mainThread(func() {
			for _, m := range ms {
				a.chatMessage(m)
			}
		})
	}
	g.OnState = func(s share.State) { a.mainThread(func() { a.chatState(s) }) }
	c.quiet = true // what was said before we came isn't news
	for _, m := range g.Chat() {
		a.chatMessage(m)
	}
	c.quiet = false
	c.unread, c.readSeq = 0, a.lastSeq()
	a.chatState(g.State())
	a.chatPeople(g.People())
}

func (a *App) chatPeople(ps []share.Participant) {
	c := a.chat
	c.people = ps
	for _, p := range ps {
		c.names[p.ID] = p.Name
	}
	c.dirty, a.forceRedraw = true, true
}

func (a *App) chatState(s share.State) {
	c := a.chat
	old := c.state
	c.state = s
	if s.You != 0 {
		c.you = s.You
	}
	if !c.host && len(old.Tabs) > 0 { // the helm came to us, or went
		for _, t := range s.Tabs {
			was := old.HelmOf(t.Window)
			switch {
			case t.Helm == c.you && was != c.you:
				a.chatToast("⎈ you have the helm of "+a.chatTabName(t.Window)+": type away", 6*time.Second)
			case was == c.you && t.Helm != c.you && old.Tab(t.Window) != nil:
				a.chatToast("⎈ "+shortName(a.personName(t.Helm))+" has the helm of "+a.chatTabName(t.Window)+" now", 6*time.Second)
			}
		}
	}
	for _, r := range s.Requests {
		mine := r.Approver == c.you || (c.host && r.Approver == 0)
		if k := fmt.Sprintf("helm/%d", r.ID); mine && !c.seen[k] {
			c.seen[k] = true
			a.chatAlert(fmt.Sprintf("%s wants the helm of %s", shortName(r.Name), a.chatTabName(r.Window)))
		}
	}
	c.dirty, a.forceRedraw = true, true
}

// chatMessage adds a message (or shows a notice meant just for us).
func (a *App) chatMessage(m share.ChatMsg) {
	c := a.chat
	if m.Seq == 0 {
		a.chatToast(m.Text, 5*time.Second)
		return
	}
	if n := len(c.msgs); n > 0 && m.Seq <= c.msgs[n-1].Seq {
		return // already have it
	}
	c.msgs = append(c.msgs, m)
	if len(c.msgs) > 500 {
		c.msgs = append([]share.ChatMsg(nil), c.msgs[len(c.msgs)-500:]...)
	}
	if m.Name != "" {
		c.names[m.From] = m.Name
	}
	c.dirty, a.forceRedraw = true, true
	own := !m.Event && m.From == c.you && m.Host == c.host
	if own || a.reading() || c.quiet {
		c.readSeq = m.Seq
		return
	}
	if m.Event {
		return
	}
	c.unread++
	first, _, _ := strings.Cut(m.Text, "\n")
	a.chatToast(shortName(m.Name)+": "+first, 6*time.Second)
	if a.mentioned(m.Text) {
		desktopNotify("germ · "+shortName(m.Name), first)
		a.win.RequestAttention()
	}
}

// mentioned reports whether text mentions us (@name or @here).
func (a *App) mentioned(text string) bool {
	me := strings.ToLower(shortName(a.chat.names[a.chat.you]))
	if a.chat.host {
		me = strings.ToLower(shortName(whoami()))
	}
	t := strings.ToLower(text)
	for _, w := range strings.FieldsFunc(t, func(r rune) bool { return unicode.IsSpace(r) || strings.ContainsRune(",.:;!?()", r) }) {
		if w == "@here" || (me != "" && w == "@"+me) {
			return true
		}
	}
	return false
}

// chatAlert is something that needs the user: bell, notification, toast.
func (a *App) chatAlert(text string) {
	if a.sharePane() != nil && !a.inSharePane() {
		text += " · Ctrl+Shift+M"
	}
	a.chatToast("» "+text, 10*time.Second)
	if !a.focused || !(a.chat.visible() || a.inSharePane()) {
		desktopNotify("germ", text)
	}
	a.win.RequestAttention()
}

// chatToast shows text in the tab bar for a while.
func (a *App) chatToast(text string, d time.Duration) {
	a.chat.toast, a.chat.toastUntil = text, time.Now().Add(d)
	a.forceRedraw = true
}

// chatTick runs about once a second.
func (a *App) chatTick() {
	c := a.chat
	if c.host && !c.lastUpdate.IsZero() && time.Since(c.lastUpdate) > chatStale {
		c.lastUpdate = time.Time{}
		a.endChat()
	}
	if c.toast != "" && time.Now().After(c.toastUntil) {
		c.toast = ""
		a.forceRedraw = true
	}
}

// chatStatus is the tab bar's right side while sharing: a toast, or the
// usual status with the unread count.
func (a *App) chatStatus(base string) string {
	c := a.chat
	if c.toast != "" && time.Now().Before(c.toastUntil) {
		return c.toast
	}
	if c.active() && c.unread > 0 {
		if base != "" {
			base += " · "
		}
		base += fmt.Sprintf("%d new (Ctrl+Shift+M)", c.unread)
	}
	return base
}

// ---- doing things ----------------------------------------------------------

// chatAct does something in the session.
func (a *App) chatAct(act share.Action) {
	c := a.chat
	switch {
	case c.guest != nil:
		select {
		case c.sendq <- act:
		default:
		}
	case c.host:
		c.pending = append(c.pending, act)
		if c.waiter != nil {
			c.waiter(&control.Response{OK: true, Actions: c.pending})
			c.waiter, c.pending = nil, nil
		}
	}
}

// send sends the message box (or runs a /command).
func (a *App) chatSend() {
	c := a.chat
	text := strings.TrimSpace(string(c.input))
	if text == "" {
		return
	}
	c.input, c.cur, c.scroll = nil, 0, 0
	c.sent = append(c.sent, text)
	c.sentAt = len(c.sent)
	c.dirty = true
	if strings.HasPrefix(text, "/") && !strings.HasPrefix(text, "//") {
		a.chatCommand(text)
		return
	}
	text = strings.TrimPrefix(text, "/") // "//x" sends "/x"
	a.chatAct(share.Action{Op: share.OpSay, Text: text})
}

// chatCommand runs a /command typed in the chat.
func (a *App) chatCommand(line string) {
	c := a.chat
	cmd, arg, _ := strings.Cut(strings.TrimPrefix(line, "/"), " ")
	arg = strings.TrimSpace(arg)
	win := a.chatWindow()
	person := func() (uint32, bool) {
		id, ok := a.personNamed(arg)
		if !ok {
			a.chatToast("nobody here called "+arg, 4*time.Second)
		}
		return id, ok
	}
	hostOnly := func() bool {
		if !c.host {
			a.chatToast("only the host can do that", 4*time.Second)
		}
		return c.host
	}
	switch cmd {
	case "save":
		if hostOnly() {
			a.chatAct(share.Action{Op: share.OpSave})
		}
	case "give":
		if hostOnly() && win != "" {
			if id, ok := person(); ok {
				a.chatAct(share.Action{Op: share.OpGive, Window: win, Person: id})
			}
		}
	case "branch":
		if hostOnly() {
			who, task, _ := strings.Cut(arg, " ")
			if _, ok := a.personNamed(who); !ok {
				a.chatToast("usage: /branch NAME [what they'll work on]", 5*time.Second)
				return
			}
			a.branchFor(shortName(who), strings.TrimSpace(task))
		}
	case "takeback", "untake":
		if hostOnly() && win != "" {
			a.chatAct(share.Action{Op: share.OpUntake, Window: win})
		}
	case "mute", "unmute":
		if hostOnly() {
			if id, ok := person(); ok {
				op := share.OpMute
				if cmd == "unmute" {
					op = share.OpUnmute
				}
				a.chatAct(share.Action{Op: op, Person: id})
			}
		}
	case "helm", "take", "release":
		a.helmKey()
	case "yes", "no":
		a.chatDecide(cmd == "yes")
	default:
		help := "/helm (ask for it, or hand it back)  /yes /no  //text sends /text"
		if c.host {
			help = "/give NAME  /branch NAME [task]  /takeback  /mute NAME  /unmute NAME  /save  " + help
		}
		a.chatToast(help, 10*time.Second)
	}
}

// chatGive gives p's tab to the person called name.
func (a *App) chatGive(p *pane, name string) error {
	c := a.chat
	if !c.active() || !c.host {
		return fmt.Errorf("this window isn't sharing a session")
	}
	t := a.tabOf(p)
	if t == nil || t.tmuxID == "" {
		return fmt.Errorf("that pane isn't in a tmux tab")
	}
	id, ok := a.personNamed(name)
	if !ok {
		return fmt.Errorf("nobody called %s is in the session", name)
	}
	a.chatAct(share.Action{Op: share.OpGive, Window: t.tmuxID, Person: id})
	return nil
}

// personNamed finds a guest by name or short name.
func (a *App) personNamed(name string) (uint32, bool) {
	for _, p := range a.chat.people {
		if !p.Host && share.NameMatches(p.Name, name) {
			return p.ID, true
		}
	}
	return 0, false
}

// branchFor opens a tab on a new branch for a guest (germ agent -for).
func (a *App) branchFor(name, task string) {
	self, err := os.Executable()
	if err != nil {
		return
	}
	argv := []string{self, "agent", "-for", name}
	if task != "" {
		argv = append(argv, task)
	}
	cwd := ""
	if p := a.workPane(); p != nil {
		cwd = paneDir(p)
	}
	noop := func(*control.Response) {}
	a.ctlOpen(control.Request{Where: "tab", Command: argv, Focus: true, Cwd: cwd}, nil, noop, noop)
}

// chatDecision is the oldest request we can answer: a join (host) or the
// helm.
func (a *App) chatDecision() (label string, act share.Action, ok bool) {
	c := a.chat
	if c.host && len(c.joins) > 0 {
		j := c.joins[0]
		return j.Name + " wants to join", share.Action{Op: share.OpJoin, Request: j.ID}, true
	}
	for _, r := range c.state.Requests {
		if r.Approver == c.you || c.host {
			return fmt.Sprintf("%s wants the helm of %s", shortName(r.Name), a.chatTabName(r.Window)),
				share.Action{Op: share.OpDecide, Request: r.ID}, true
		}
	}
	return "", share.Action{}, false
}

func (a *App) chatDecide(allow bool) {
	if _, act, ok := a.chatDecision(); ok {
		act.Allow = allow
		a.chatAct(act)
		// Don't ask again while germ share catches up.
		c := a.chat
		if act.Op == share.OpJoin && len(c.joins) > 0 {
			c.joins = c.joins[1:]
		} else {
			for i, r := range c.state.Requests {
				if r.ID == act.Request {
					c.state.Requests = append(c.state.Requests[:i:i], c.state.Requests[i+1:]...)
					break
				}
			}
		}
		c.dirty, a.forceRedraw = true, true
	} else {
		a.chatToast("nothing to answer", 3*time.Second)
	}
}

// chatWindow is the tmux window of the tab being looked at.
func (a *App) chatWindow() string {
	if t := a.tab(); t != nil {
		return t.tmuxID
	}
	return ""
}

// chatTabName is "tab 2 (zsh)".
func (a *App) chatTabName(window string) string {
	if t := a.chat.state.Tab(window); t != nil {
		if t.Name != "" {
			return fmt.Sprintf("tab %d (%s)", t.Index, t.Name)
		}
		return fmt.Sprintf("tab %d", t.Index)
	}
	return "a tab"
}

func (a *App) personName(id uint32) string {
	if id == 0 && a.chat.host {
		return whoami()
	}
	for _, p := range a.chat.people {
		if p.ID == id {
			return p.Name
		}
	}
	if n, ok := a.chat.names[id]; ok {
		return n
	}
	if id == 0 {
		return "the host"
	}
	return "someone"
}

// helmKey takes the helm of this tab (host), or asks for it or hands it
// back (guests).
func (a *App) helmKey() bool {
	if !a.chat.active() {
		return false
	}
	if win := a.chatWindow(); win != "" {
		a.chatToast(a.helmAct(win), 4*time.Second)
	}
	return true
}

// helmAct is helmKey for the tab of tmux window win, returning what
// happened.
func (a *App) helmAct(win string) string {
	c := a.chat
	holder := c.state.HelmOf(win)
	if c.host {
		if holder == 0 {
			return "⎈ you have the helm here"
		}
		a.chatAct(share.Action{Op: share.OpTake, Window: win})
		return "⎈ you took the helm back from " + shortName(a.personName(holder))
	}
	if holder == c.you {
		a.chatAct(share.Action{Op: share.OpRelease, Window: win})
		return "⎈ you handed the helm back"
	}
	for _, r := range c.state.Requests {
		if r.From == c.you && r.Window == win {
			return "⎈ still waiting for " + shortName(a.personName(r.Approver))
		}
	}
	a.chatAct(share.Action{Op: share.OpRequest, Window: win})
	decider := uint32(0) // the tab's owner decides, else the host
	if t := c.state.Tab(win); t != nil {
		decider = t.Owner
	}
	return "⎈ asked " + shortName(a.personName(decider)) + " for the helm…"
}

// ctlHelm is germ helm: the helm of p's tab (the active tab if p is nil).
func (a *App) ctlHelm(p *pane, line string) (string, error) {
	c := a.chat
	if !c.active() {
		return "", fmt.Errorf("this window isn't in a shared session")
	}
	win := a.chatWindow()
	if p != nil {
		if t := a.tabOf(p); t != nil && t.tmuxID != "" {
			win = t.tmuxID
		}
	}
	args := strings.Fields(line)
	sub := ""
	if len(args) > 0 {
		sub, args = args[0], args[1:]
	}
	tabArg := func(i int) (string, error) {
		if len(args) <= i {
			if win == "" {
				return "", fmt.Errorf("which tab? give its number")
			}
			return win, nil
		}
		for _, t := range c.state.Tabs {
			if strings.TrimPrefix(args[i], "#") == fmt.Sprint(t.Index) {
				return t.Window, nil
			}
		}
		return "", fmt.Errorf("no tab %s", args[i])
	}
	host := func() error {
		if !c.host {
			return fmt.Errorf("only the host can do that")
		}
		return nil
	}
	switch sub {
	case "":
		if win == "" {
			return "", fmt.Errorf("which tab? give its number")
		}
		return a.helmAct(win), nil
	case "status", "who":
		var b strings.Builder
		for _, t := range c.state.Tabs {
			who := shortName(a.personName(t.Helm))
			if t.Helm == c.you {
				who = "you"
			}
			fmt.Fprintf(&b, "%d %s: %s", t.Index, t.Name, who)
			if t.Owner != 0 {
				fmt.Fprintf(&b, " (%s's tab)", shortName(a.personName(t.Owner)))
			}
			b.WriteString("\n")
		}
		return strings.TrimSuffix(b.String(), "\n"), nil
	case "ask":
		if c.host {
			return "", fmt.Errorf("you're the host: germ helm take takes it back")
		}
		w, err := tabArg(0)
		if err != nil {
			return "", err
		}
		if c.state.HelmOf(w) == c.you {
			return "⎈ you have the helm here", nil
		}
		return a.helmAct(w), nil
	case "back":
		w, err := tabArg(0)
		if err != nil {
			return "", err
		}
		if c.host || c.state.HelmOf(w) != c.you {
			return "", fmt.Errorf("you don't have the helm there")
		}
		return a.helmAct(w), nil
	case "give", "hand":
		if err := host(); err != nil {
			return "", err
		}
		if len(args) == 0 {
			return "", fmt.Errorf("usage: germ helm give NAME [TAB]")
		}
		id, ok := a.personNamed(args[0])
		if !ok {
			return "", fmt.Errorf("nobody called %s is in the session", args[0])
		}
		w, err := tabArg(1)
		if err != nil {
			return "", err
		}
		a.chatAct(share.Action{Op: share.OpHand, Window: w, Person: id})
		return "⎈ gave " + shortName(a.personName(id)) + " the helm of " + a.chatTabName(w), nil
	case "take":
		if err := host(); err != nil {
			return "", err
		}
		var wins []string
		if len(args) > 0 && args[0] == "all" || len(args) == 0 && c.state.HelmOf(win) == 0 {
			// all, or no tab given and a guest doesn't have this one: wherever one does
			for _, t := range c.state.Tabs {
				wins = append(wins, t.Window)
			}
		} else {
			w, err := tabArg(0)
			if err != nil {
				return "", err
			}
			wins = []string{w}
		}
		n := 0
		for _, w := range wins {
			if c.state.HelmOf(w) != 0 {
				a.chatAct(share.Action{Op: share.OpTake, Window: w})
				n++
			}
		}
		if n == 0 {
			return "⎈ you already have the helm", nil
		}
		return fmt.Sprintf("⎈ took the helm back (%d tab%s)", n, map[bool]string{true: "s"}[n > 1]), nil
	}
	return "", fmt.Errorf("usage: germ helm [status|ask|back|give NAME [TAB]|take [TAB|all]]")
}

// helmBlocked says why we can't type in p ("" if we can): someone else
// has its tab's helm.
func (a *App) helmBlocked(p *pane) string {
	c := a.chat
	if !c.active() || p == nil {
		return ""
	}
	t := a.tabOf(p)
	if t == nil || t.tmuxID == "" || p == a.sharePane() { // the share pane is always the host's
		return ""
	}
	holder := c.state.HelmOf(t.tmuxID)
	if holder == c.you {
		return ""
	}
	who := shortName(a.personName(holder))
	if c.host {
		return "⎈ " + who + " has the helm here · Enter takes it back"
	}
	for _, r := range c.state.Requests {
		if r.From == c.you && r.Window == t.tmuxID {
			return "⎈ waiting for " + shortName(a.personName(r.Approver)) + " to hand you the helm…"
		}
	}
	return "⎈ " + who + " has the helm here · Enter asks for it"
}

// helmTag marks tabs someone other than the host types in: "⎈alice".
func (a *App) helmTag(t *tab) string {
	c := a.chat
	if !c.active() || t.tmuxID == "" {
		return ""
	}
	st := c.state.Tab(t.tmuxID)
	if st == nil || st.Helm == 0 {
		return ""
	}
	if st.Helm == c.you {
		return "⎈you"
	}
	return "⎈" + shortName(a.personName(st.Helm))
}

// chatPaletteItems are the session's commands in the palette: the host
// gives tabs away and takes them back, mutes, saves the chat.
func (a *App) chatPaletteItems() []paletteItem {
	c := a.chat
	if !c.active() || !c.host {
		return nil
	}
	var items []paletteItem
	win := a.chatWindow()
	if t := c.state.Tab(win); win != "" && t != nil {
		for _, p := range c.people {
			if p.Host || t.Owner == p.ID {
				continue
			}
			id := p.ID
			items = append(items, paletteItem{label: "Give this tab to " + p.Name,
				run: func(a *App) { a.chatAct(share.Action{Op: share.OpGive, Window: win, Person: id}) }})
		}
		if t.Owner != 0 || t.Helm != 0 {
			items = append(items, paletteItem{label: "Take this tab back from " + a.personName(max(t.Owner, t.Helm)),
				run: func(a *App) { a.chatAct(share.Action{Op: share.OpUntake, Window: win}) }})
		}
	}
	for _, p := range c.people {
		if p.Host {
			continue
		}
		name := shortName(p.Name)
		items = append(items, paletteItem{label: "New tab for " + name + " on its own branch",
			run: func(a *App) { a.branchFor(name, "") }})
		id, op, verb := p.ID, share.OpMute, "Mute "
		if p.Muted {
			op, verb = share.OpUnmute, "Unmute "
		}
		items = append(items, paletteItem{label: verb + p.Name + " in chat",
			run: func(a *App) { a.chatAct(share.Action{Op: op, Person: id}) }})
	}
	items = append(items, paletteItem{label: "Save the chat to a file",
		run: func(a *App) { a.chatAct(share.Action{Op: share.OpSave}) }})
	return items
}

// ---- keys ------------------------------------------------------------------

// chatKey handles a key while the panel is focused, reporting whether it
// did something with it.
func (a *App) chatKey(key glfw.Key, mods glfw.ModifierKey) bool {
	c := a.chat
	ctrl := mods&glfw.ModControl != 0
	shift := mods&glfw.ModShift != 0
	if ctrl && shift {
		return false // germ's shortcuts
	}
	c.dirty, a.forceRedraw = true, true
	switch {
	case key == glfw.KeyEscape:
		c.focused = false
	case key == glfw.KeyEnter || key == glfw.KeyKPEnter:
		if shift {
			a.chatInsert("\n")
		} else {
			a.chatSend()
		}
	case key == glfw.KeyBackspace:
		if ctrl {
			a.chatDeleteWord()
		} else if c.cur > 0 {
			c.input = append(c.input[:c.cur-1], c.input[c.cur:]...)
			c.cur--
		}
	case key == glfw.KeyDelete:
		if c.cur < len(c.input) {
			c.input = append(c.input[:c.cur], c.input[c.cur+1:]...)
		}
	case key == glfw.KeyLeft:
		c.cur = max(0, c.cur-1)
	case key == glfw.KeyRight:
		c.cur = min(len(c.input), c.cur+1)
	case key == glfw.KeyHome || (ctrl && key == glfw.KeyA):
		c.cur = 0
	case key == glfw.KeyEnd || (ctrl && key == glfw.KeyE):
		c.cur = len(c.input)
	case ctrl && key == glfw.KeyU:
		c.input, c.cur = c.input[c.cur:], 0
	case ctrl && key == glfw.KeyK:
		c.input = c.input[:c.cur]
	case ctrl && key == glfw.KeyW:
		a.chatDeleteWord()
	case ctrl && key == glfw.KeyV:
		a.chatInsert(glfw.GetClipboardString())
	case key == glfw.KeyUp && (len(c.input) == 0 || c.sentAt < len(c.sent)):
		if c.sentAt > 0 {
			c.sentAt--
			c.input = []rune(c.sent[c.sentAt])
			c.cur = len(c.input)
		}
	case key == glfw.KeyDown && c.sentAt < len(c.sent):
		c.sentAt++
		c.input = nil
		if c.sentAt < len(c.sent) {
			c.input = []rune(c.sent[c.sentAt])
		}
		c.cur = len(c.input)
	case key == glfw.KeyPageUp:
		c.scroll += 10
	case key == glfw.KeyPageDown:
		c.scroll = max(0, c.scroll-10)
		if c.scroll == 0 {
			a.markRead()
		}
	default:
		return ctrl // other Ctrl keys do nothing here; plain keys arrive as text
	}
	return true
}

// chatType handles typed text while the panel is focused. y and n answer
// a pending request when the box is empty.
func (a *App) chatType(r rune) {
	c := a.chat
	if len(c.input) == 0 {
		if _, _, ok := a.chatDecision(); ok && strings.ContainsRune("yYnN", r) {
			a.chatDecide(r == 'y' || r == 'Y')
			return
		}
	}
	a.chatInsert(string(r))
}

func (a *App) chatInsert(text string) {
	c := a.chat
	var rs []rune
	for _, r := range strings.ReplaceAll(text, "\r\n", "\n") {
		if r == '\t' {
			r = ' '
		}
		if r == '\n' || !unicode.IsControl(r) {
			rs = append(rs, r)
		}
	}
	if len(rs) == 0 || len(c.input)+len(rs) > 4000 {
		return
	}
	c.input = append(c.input[:c.cur], append(rs, c.input[c.cur:]...)...)
	c.cur += len(rs)
	c.dirty, a.forceRedraw = true, true
	if time.Since(c.lastComposing) > 2*time.Second {
		c.lastComposing = time.Now()
		a.chatAct(share.Action{Op: share.OpComposing})
	}
}

func (a *App) chatDeleteWord() {
	c := a.chat
	i := c.cur
	for i > 0 && unicode.IsSpace(c.input[i-1]) {
		i--
	}
	for i > 0 && !unicode.IsSpace(c.input[i-1]) {
		i--
	}
	c.input = append(c.input[:i], c.input[c.cur:]...)
	c.cur = i
}

// ---- mouse -----------------------------------------------------------------

// chatHit reports whether grid cell (gx, gy) is in the panel, and which
// panel row.
func (a *App) chatHit(gx, gy int) (row int, ok bool) {
	w := a.chatCols()
	if w == 0 || gx < a.gridCols-w || gy < a.barRows() {
		return 0, false
	}
	return gy - a.barRows(), true
}

// chatClick focuses the panel and opens a link clicked in it.
func (a *App) chatClick(row int) {
	c := a.chat
	c.focused, c.dirty, a.forceRedraw = true, true, true
	if row < 0 || row >= len(c.rowLinks) {
		return
	}
	col := a.mouse.gx - (a.gridCols - a.chatCols() + 1)
	text := c.rowLinks[row]
	for _, m := range urlMatches(text) {
		if col >= runewidth.StringWidth(text[:m[0]]) && col < runewidth.StringWidth(text[:m[1]]) {
			openURL(text[m[0]:m[1]])
		}
	}
}

func (a *App) chatScroll(notches int) {
	c := a.chat
	c.scroll = max(0, c.scroll+3*notches)
	if c.scroll == 0 && c.focused {
		a.markRead()
	}
	c.dirty, a.forceRedraw = true, true
}

// ---- drawing ---------------------------------------------------------------

// chatSurface brings the panel up to date and returns its surface (nil
// when hidden) and the separator's rectangle.
func (a *App) chatSurface(th *theme.Theme) (*render.Surface, []render.Rect) {
	w := a.chatCols()
	if w == 0 {
		return nil, nil
	}
	c := a.chat
	cols, rows := w-1, a.gridRows-a.barRows()
	if c.term == nil {
		c.term = term.New(cols, rows, term.Options{Theme: th})
		c.dirty = true
	}
	if cc, rr := c.term.Size(); cc != cols || rr != rows {
		c.term.Resize(cols, rows)
		c.dirty = true
	}
	if c.theme != th {
		c.term.SetTheme(th)
		c.theme = th
		c.dirty = true
	}
	if c.dirty {
		c.dirty = false
		out, links := chatview.Render(a.chatView(), cols, rows)
		c.rowLinks = links
		c.term.Write([]byte(out))
	}
	cw, ch := a.render.CellSize()
	pad := a.render.Pad()
	x0 := pad + (a.gridCols-w)*cw
	oy := pad + a.barRows()*ch
	c.surf.Update(c.term, render.View{Focused: c.focused && a.focused, Force: a.forceRedraw}, x0+cw, oy, cw/2)
	sep := render.Rect{X: x0 + cw/2 - max(1, cw/8)/2, Y: oy, W: max(1, cw/8), H: rows * ch,
		Color: blendRGB(th.Background, th.Foreground, 0.25)}
	return c.surf, []render.Rect{sep}
}

// chatView is what the panel draws.
func (a *App) chatView() chatview.View {
	c := a.chat
	v := chatview.View{Msgs: c.msgs, People: c.people, You: c.you, Host: c.host, Input: c.input, Cur: c.cur,
		Focused: c.focused, Scroll: c.scroll, Divider: c.divider}
	if c.host {
		v.Me = shortName(whoami())
	} else {
		v.Me = shortName(c.names[c.you])
	}
	if label, _, ok := a.chatDecision(); ok {
		v.Decision = label
		n := 0
		for _, r := range c.state.Requests {
			if r.Approver == c.you || c.host {
				n++
			}
		}
		if c.host {
			n += len(c.joins)
		}
		v.MoreAsks = n - 1
	}
	for _, p := range c.people {
		if p.Composing && !(p.ID == c.you && p.Host == c.host) {
			v.Typing = append(v.Typing, shortName(p.Name))
		}
	}
	if !c.host {
		for _, t := range c.state.Tabs {
			if t.Helm == c.you {
				v.MyHelm = fmt.Sprintf("⎈ yours: %s", a.chatTabName(t.Window))
				break
			}
		}
	}
	return v
}

func shortName(n string) string { return share.ShortName(n) }

// whoami is how the host appears in the session (as germ share names it).
func whoami() string {
	user := os.Getenv("USER")
	if user == "" {
		user = "someone"
	}
	if host, _ := os.Hostname(); host != "" {
		return user + "@" + host
	}
	return user
}

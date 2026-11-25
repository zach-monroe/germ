package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/sys/unix"

	"github.com/zach-monroe/germ/internal/chatview"
	"github.com/zach-monroe/germ/internal/control"
	"github.com/zach-monroe/germ/internal/share"
)

// sharePane is the host's one place for a shared session: the invite
// link, who's here, who has the helm of each tab, the chat with a message
// box, and the questions to answer (joins, the helm). Everything else is a
// /command typed in the box.
type sharePane struct {
	h       *share.Host
	br      *shareBridge
	ctl     *control.Client // the window, for /branch (nil without one)
	session string
	via     string
	stop    func() // stop sharing

	mu          sync.Mutex
	msgs        []share.ChatMsg // the chat, and this pane's own log lines
	input       []rune
	cur         int
	sent        []string
	sentAt      int
	scroll      int
	notice      string
	noticeUntil time.Time
	quitting    bool // asking "stop sharing?"
	composedAt  time.Time
	rung        map[string]bool // requests already announced

	outMu  sync.Mutex // one writer to the terminal
	redraw chan struct{}
}

func newSharePane(h *share.Host, br *shareBridge, ctl *control.Client, session, via string, stop func()) *sharePane {
	return &sharePane{h: h, br: br, ctl: ctl, session: session, via: via, stop: stop,
		rung: map[string]bool{}, redraw: make(chan struct{}, 1)}
}

func (p *sharePane) changed() {
	select {
	case p.redraw <- struct{}{}:
	default:
	}
}

// log adds a line to the pane's log (not the chat: only the host sees it).
func (p *sharePane) log(format string, a ...any) {
	p.mu.Lock()
	p.msgs = append(p.msgs, share.ChatMsg{Event: true, Text: fmt.Sprintf(format, a...), At: time.Now().UnixMilli()})
	p.trim()
	p.mu.Unlock()
	p.changed()
}

// chat adds a chat message. Caller doesn't hold mu.
func (p *sharePane) chat(m share.ChatMsg) {
	p.mu.Lock()
	p.msgs = append(p.msgs, m)
	p.trim()
	p.mu.Unlock()
	p.changed()
}

func (p *sharePane) trim() {
	if len(p.msgs) > 1000 {
		p.msgs = append([]share.ChatMsg(nil), p.msgs[len(p.msgs)-1000:]...)
	}
}

// say shows a short message on the status row for a few seconds.
func (p *sharePane) say(format string, a ...any) {
	p.mu.Lock()
	p.notice, p.noticeUntil = fmt.Sprintf(format, a...), time.Now().Add(6*time.Second)
	p.mu.Unlock()
	p.changed()
}

// asked rings for a new question, unless the window does (it rings and
// notifies itself).
func (p *sharePane) asked(key, question string) {
	p.mu.Lock()
	seen := p.rung[key]
	p.rung[key] = true
	p.mu.Unlock()
	if seen {
		return
	}
	if !p.br.window() {
		p.write("\a")
		go notify(question)
	}
	p.changed()
}

// decision is the oldest question to answer: stopping, a join, or the helm.
func (p *sharePane) decision() (label string, answer func(bool), n int) {
	p.mu.Lock()
	quitting := p.quitting
	p.mu.Unlock()
	if quitting {
		return "Stop sharing and disconnect everyone?", func(yes bool) {
			p.mu.Lock()
			p.quitting = false
			p.mu.Unlock()
			if yes {
				p.stop()
			}
		}, 1
	}
	joins := p.h.JoinRequests()
	st := p.h.State()
	n = len(joins) + len(st.Requests)
	if len(joins) > 0 {
		j := joins[0]
		where := ""
		if j.Web {
			where = " (browser)"
		}
		return fmt.Sprintf("%s%s wants to join", j.Name, where), func(yes bool) {
			if !p.h.Decide(j.ID, yes) {
				p.say("they'd already gone")
			}
		}, n
	}
	if len(st.Requests) > 0 {
		r := st.Requests[0]
		return fmt.Sprintf("%s asks for the helm of %s", share.ShortName(r.Name), tabName(st, r.Window)), func(yes bool) {
			p.h.Act(share.Action{Op: share.OpDecide, Request: r.ID, Allow: yes})
		}, n
	}
	return "", nil, 0
}

// ---- drawing ---------------------------------------------------------------

func (p *sharePane) write(s string) {
	p.outMu.Lock()
	os.Stdout.WriteString(s)
	p.outMu.Unlock()
}

func (p *sharePane) draw() {
	cols, rows := termSize()
	label, _, n := p.decision()
	people := p.h.Participants()
	st := p.h.State()
	p.mu.Lock()
	if p.notice != "" && time.Now().After(p.noticeUntil) {
		p.notice = ""
	}
	v := chatview.View{
		Msgs: p.msgs, People: people, Host: true, Me: share.ShortName(p.h.Name),
		Input: p.input, Cur: p.cur, Focused: true, Scroll: p.scroll,
		Decision: label, MoreAsks: n - 1, Notice: p.notice,
		Hint: "message · /help for commands",
		Head: p.head(people, st, cols),
	}
	p.mu.Unlock()
	for _, pp := range people {
		if pp.Composing && !pp.Host {
			v.Typing = append(v.Typing, share.ShortName(pp.Name))
		}
	}
	out, _ := chatview.Render(v, cols, rows)
	p.write(out)
}

// head is the rows above the chat: the session, the link, who's here
// (numbered, for /kick) and who has the helm of each tab.
func (p *sharePane) head(people []share.Participant, st share.State, cols int) []string {
	const b, d, r = "\x1b[1m", "\x1b[2m", "\x1b[0m"
	rows := []string{
		chatview.Fit(fmt.Sprintf("%s\x1b[34m◆ contagious%s%s · session %q %s · /copy copies the link", b, r, d, p.session, p.via), cols),
	}
	// The link in full, wrapped (cut, it wouldn't work when copied).
	link := []rune(p.h.Link().String())
	for len(link) > 0 && cols > 0 {
		n := min(len(link), cols)
		rows = append(rows, "\x1b[36m"+string(link[:n]))
		link = link[n:]
	}
	names := map[uint32]string{0: "you"}
	var here []string
	for i, pp := range people {
		if pp.Host {
			continue
		}
		names[pp.ID] = share.ShortName(pp.Name)
		s := fmt.Sprintf("%s%d%s %s", b, i, r, share.ShortName(pp.Name))
		if pp.Web {
			s += d + " (browser)" + r
		}
		if pp.Typing {
			s += " ✎"
		}
		if pp.Muted {
			s += d + " (muted)" + r
		}
		here = append(here, s)
	}
	if len(here) == 0 {
		rows = append(rows, d+"nobody's here yet: send them the link")
	} else {
		rows = append(rows, chatview.Fit("here: "+strings.Join(here, "  "), cols))
	}
	var tabs []string
	for _, t := range st.Tabs {
		who := names[t.Helm]
		if who == "" {
			who = "someone"
		}
		s := fmt.Sprintf("%s%d%s %s: ", b, t.Index, r, t.Name)
		if t.Helm == 0 {
			s += d + "you" + r
		} else {
			s += "\x1b[33m" + who + r
		}
		if t.Owner != 0 {
			s += d + " (" + names[t.Owner] + "'s tab)" + r
		}
		if t.Window == st.Current {
			s = "\x1b[4m" + s + r // the tab /helm and /take act on by default
		}
		tabs = append(tabs, s)
	}
	if len(tabs) > 0 {
		rows = append(rows, chatview.Fit("⎈ "+strings.Join(tabs, d+" · "+r), cols))
	}
	return rows
}

// ---- running ---------------------------------------------------------------

// run shows the pane until sharing stops (ctx is done).
func (p *sharePane) run(ctx context.Context) {
	tty := isTTY(0)
	var old *unix.Termios
	if tty {
		if t, err := unix.IoctlGetTermios(0, unix.TCGETS); err == nil {
			old = t
			raw := *t
			raw.Lflag &^= unix.ICANON | unix.ECHO | unix.ISIG | unix.IEXTEN
			raw.Iflag &^= unix.IXON | unix.ICRNL
			raw.Cc[unix.VMIN], raw.Cc[unix.VTIME] = 1, 0
			unix.IoctlSetTermios(0, unix.TCSETS, &raw)
		}
	}
	p.write("\x1b[?1049h\x1b[?2004h")
	defer func() {
		p.write("\x1b[?2004l\x1b[?25h\x1b[?1049l")
		if old != nil {
			unix.IoctlSetTermios(0, unix.TCSETS, old)
		}
	}()

	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	defer signal.Stop(winch)
	input := make(chan []byte, 16)
	if tty {
		go func() {
			buf := make([]byte, 4096)
			for {
				n, err := os.Stdin.Read(buf)
				if err != nil || n == 0 {
					close(input)
					return
				}
				input <- append([]byte(nil), buf[:n]...)
			}
		}()
	}
	tick := time.NewTicker(time.Second) // notices expire, requests time out
	defer tick.Stop()
	p.draw()
	var pasting []byte
	for {
		select {
		case <-ctx.Done():
			return
		case <-winch:
		case <-tick.C:
		case <-p.redraw:
			time.Sleep(20 * time.Millisecond) // coalesce bursts
		case b, ok := <-input:
			if !ok {
				input = nil
				continue
			}
			pasting = p.keys(b, pasting)
		}
		p.draw()
	}
}

// keys handles bytes from the terminal. pasting holds a bracketed paste in
// progress; the rest of it is returned.
func (p *sharePane) keys(b []byte, pasting []byte) []byte {
	const pasteStart, pasteEnd = "\x1b[200~", "\x1b[201~"
	if pasting != nil {
		pasting = append(pasting, b...)
		if i := strings.Index(string(pasting), pasteEnd); i >= 0 {
			p.insert(string(pasting[:i]))
			rest := pasting[i+len(pasteEnd):]
			if len(rest) > 0 {
				return p.keys(rest, nil)
			}
			return nil
		}
		return pasting
	}
	s := string(b)
	for len(s) > 0 {
		if strings.HasPrefix(s, pasteStart) {
			return p.keys([]byte(s[len(pasteStart):]), []byte{})
		}
		if s[0] == '\x1b' {
			n := escLen(s)
			p.escape(s[:n])
			s = s[n:]
			continue
		}
		r, n := utf8.DecodeRuneInString(s)
		s = s[n:]
		p.key(r)
	}
	return nil
}

// escLen is the length of the escape sequence at the start of s (just
// "\x1b" for a lone Esc).
func escLen(s string) int {
	if len(s) < 2 {
		return len(s)
	}
	switch s[1] {
	case '[':
		for i := 2; i < len(s); i++ {
			if s[i] >= 0x40 && s[i] <= 0x7e {
				return i + 1
			}
		}
		return len(s)
	case 'O':
		return min(3, len(s))
	}
	return 1
}

func (p *sharePane) escape(seq string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch seq {
	case "\x1b":
		p.quitting = false
	case "\x1b[D", "\x1bOD":
		p.cur = max(0, p.cur-1)
	case "\x1b[C", "\x1bOC":
		p.cur = min(len(p.input), p.cur+1)
	case "\x1b[H", "\x1bOH", "\x1b[1~":
		p.cur = 0
	case "\x1b[F", "\x1bOF", "\x1b[4~":
		p.cur = len(p.input)
	case "\x1b[3~":
		if p.cur < len(p.input) {
			p.input = append(p.input[:p.cur], p.input[p.cur+1:]...)
		}
	case "\x1b[A", "\x1bOA":
		if p.sentAt > 0 && (len(p.input) == 0 || p.sentAt < len(p.sent)) {
			p.sentAt--
			p.input = []rune(p.sent[p.sentAt])
			p.cur = len(p.input)
		}
	case "\x1b[B", "\x1bOB":
		if p.sentAt < len(p.sent) {
			p.sentAt++
			p.input = nil
			if p.sentAt < len(p.sent) {
				p.input = []rune(p.sent[p.sentAt])
			}
			p.cur = len(p.input)
		}
	case "\x1b[5~":
		p.scroll += 10
	case "\x1b[6~":
		p.scroll = max(0, p.scroll-10)
	}
}

func (p *sharePane) key(r rune) {
	p.mu.Lock()
	empty := len(p.input) == 0
	p.mu.Unlock()
	switch r {
	case '\r', '\n':
		p.send()
		return
	case 3, 4: // Ctrl+C, Ctrl+D: clear the box, or ask to stop sharing
		p.mu.Lock()
		if len(p.input) > 0 {
			p.input, p.cur = nil, 0
		} else {
			p.quitting = true
		}
		p.mu.Unlock()
		return
	}
	if empty && strings.ContainsRune("yYnN", r) {
		if _, answer, _ := p.decision(); answer != nil {
			answer(r == 'y' || r == 'Y')
			return
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	switch r {
	case 127, 8: // Backspace
		if p.cur > 0 {
			p.input = append(p.input[:p.cur-1], p.input[p.cur:]...)
			p.cur--
		}
	case 1: // Ctrl+A
		p.cur = 0
	case 5: // Ctrl+E
		p.cur = len(p.input)
	case 21: // Ctrl+U
		p.input, p.cur = p.input[p.cur:], 0
	case 11: // Ctrl+K
		p.input = p.input[:p.cur]
	case 23: // Ctrl+W
		i := p.cur
		for i > 0 && unicode.IsSpace(p.input[i-1]) {
			i--
		}
		for i > 0 && !unicode.IsSpace(p.input[i-1]) {
			i--
		}
		p.input, p.cur = append(p.input[:i], p.input[p.cur:]...), i
	default:
		if !unicode.IsControl(r) {
			p.insertLocked(string(r))
		}
	}
}

func (p *sharePane) insert(text string) {
	p.mu.Lock()
	p.insertLocked(text)
	p.mu.Unlock()
}

func (p *sharePane) insertLocked(text string) {
	var rs []rune
	for _, r := range strings.ReplaceAll(text, "\r\n", "\n") {
		if r == '\t' || r == '\r' {
			r = ' '
		}
		if r == '\n' || !unicode.IsControl(r) {
			rs = append(rs, r)
		}
	}
	if len(rs) == 0 || len(p.input)+len(rs) > 4000 {
		return
	}
	p.input = append(p.input[:p.cur], append(rs, p.input[p.cur:]...)...)
	p.cur += len(rs)
	p.scroll = 0
	if time.Since(p.composedAt) > 2*time.Second {
		p.composedAt = time.Now()
		go p.h.Act(share.Action{Op: share.OpComposing})
	}
}

// send sends the message box, or runs the /command in it.
func (p *sharePane) send() {
	p.mu.Lock()
	text := strings.TrimSpace(string(p.input))
	if text != "" {
		p.input, p.cur, p.scroll = nil, 0, 0
		p.sent = append(p.sent, text)
		p.sentAt = len(p.sent)
	}
	p.mu.Unlock()
	switch {
	case text == "":
	case strings.HasPrefix(text, "/") && !strings.HasPrefix(text, "//"):
		p.command(text)
	default:
		p.h.Act(share.Action{Op: share.OpSay, Text: strings.TrimPrefix(text, "/")}) // "//x" sends "/x"
	}
}

// ---- commands ----------------------------------------------------------------

const shareHelp = `/copy · copy the invite link      /newlink · a new link (the old one stops working)
/helm NAME [TAB] · hand NAME the helm (of TAB; default the underlined tab)
/take [TAB|all] · take the helm back       /yes /no · answer (or y/n, box empty)
/give NAME [TAB] · make it NAME's tab       /takeback [TAB] · take a given tab back
/branch NAME [task] · a tab on a new branch for NAME
/kick NAME|N · remove someone   /mute NAME   /unmute NAME   /save · save the chat
/stop · stop sharing (or Ctrl+C)            //text · send a message starting with /`

func (p *sharePane) command(line string) {
	cmd, arg, _ := strings.Cut(strings.TrimPrefix(line, "/"), " ")
	args := strings.Fields(arg)
	st := p.h.State()
	switch cmd {
	case "help", "h", "?":
		for _, l := range strings.Split(shareHelp, "\n") {
			p.log("%s", l)
		}
	case "copy", "c", "link":
		p.copyLink()
		p.say("copied the invite link")
	case "newlink", "rotate":
		if err := p.h.RotateLinks(); err != nil {
			p.say("couldn't make a new link: %v", err)
			return
		}
		p.copyLink()
		p.br.changed()
		p.say("new link copied: the old one no longer works (people here stay)")
	case "save":
		p.say("%s", saveChat(p.h, p.br))
	case "stop", "quit", "q":
		p.stop()
	case "yes", "y", "no", "n":
		if _, answer, _ := p.decision(); answer != nil {
			answer(cmd[0] == 'y')
		} else {
			p.say("nothing to answer")
		}
	case "kick", "remove":
		if pp, ok := p.person(args, 0); ok {
			p.h.Remove(pp.ID)
			p.say("removed %s · /newlink so they can't come back", share.ShortName(pp.Name))
		}
	case "mute", "unmute":
		if pp, ok := p.person(args, 0); ok {
			op := share.OpMute
			if cmd == "unmute" {
				op = share.OpUnmute
			}
			p.h.Act(share.Action{Op: op, Person: pp.ID})
		}
	case "helm", "hand", "give":
		pp, ok := p.person(args, 0)
		if !ok {
			return
		}
		t, ok := p.tab(st, args, 1)
		if !ok {
			return
		}
		op := share.OpHand
		if cmd == "give" {
			op = share.OpGive
		}
		p.h.Act(share.Action{Op: op, Window: t.Window, Person: pp.ID})
	case "take", "takeback", "untake":
		var tabs []share.Tab
		if len(args) > 0 && args[0] == "all" {
			tabs = st.Tabs
		} else if t, ok := p.tab(st, args, 0); ok {
			tabs = []share.Tab{*t}
		} else {
			return
		}
		did := false
		for _, t := range tabs {
			switch {
			case cmd == "take" && t.Helm != 0:
				p.h.Act(share.Action{Op: share.OpTake, Window: t.Window})
				did = true
			case cmd != "take" && (t.Owner != 0 || t.Helm != 0):
				p.h.Act(share.Action{Op: share.OpUntake, Window: t.Window})
				did = true
			}
		}
		if !did {
			p.say("you already have the helm there")
		}
	case "branch":
		pp, ok := p.person(args, 0)
		if !ok {
			return
		}
		p.branch(share.ShortName(pp.Name), strings.Join(args[1:], " "))
	default:
		p.say("no command /%s · /help lists them", cmd)
	}
}

func (p *sharePane) copyLink() {
	p.write(fmt.Sprintf("\x1b]52;c;%s\x07", base64.StdEncoding.EncodeToString([]byte(p.h.Link().String()))))
}

// person finds a guest by name, short name or number (as listed in the
// header) in args[i].
func (p *sharePane) person(args []string, i int) (share.Participant, bool) {
	if len(args) <= i {
		p.say("whom? · /help")
		return share.Participant{}, false
	}
	want := strings.ToLower(strings.TrimPrefix(args[i], "@"))
	ps := p.h.Participants()
	if n, err := strconv.Atoi(want); err == nil && n >= 1 && n < len(ps) {
		return ps[n], true
	}
	for _, pp := range ps {
		if !pp.Host && (strings.ToLower(pp.Name) == want || strings.ToLower(share.ShortName(pp.Name)) == want) {
			return pp, true
		}
	}
	p.say("nobody here called %s", args[i])
	return share.Participant{}, false
}

// tab finds the tab numbered args[i] (by its tmux index), or the host's
// current tab without one.
func (p *sharePane) tab(st share.State, args []string, i int) (*share.Tab, bool) {
	if len(args) <= i {
		if t := st.Tab(st.Current); t != nil {
			return t, true
		}
		if len(st.Tabs) == 1 {
			return &st.Tabs[0], true
		}
		p.say("which tab? give its number")
		return nil, false
	}
	n, err := strconv.Atoi(strings.TrimPrefix(args[i], "#"))
	for j := range st.Tabs {
		if err == nil && st.Tabs[j].Index == n {
			return &st.Tabs[j], true
		}
	}
	p.say("no tab %s", args[i])
	return nil, false
}

// branch opens a tab on a new branch for a guest (germ agent -for), in
// the directory of the host's current tab.
func (p *sharePane) branch(name, task string) {
	self, err := os.Executable()
	if err != nil {
		p.say("%v", err)
		return
	}
	argv := []string{self, "agent", "-for", name}
	if task != "" {
		argv = append(argv, task)
	}
	cwd, _ := exec.Command("tmux", "display", "-p", "-t", p.session, "#{pane_current_path}").Output()
	if self := os.Getenv("TMUX_PANE"); self != "" { // we're the active pane: use the one used before
		out, _ := exec.Command("tmux", "list-panes", "-t", self, "-F", "#{pane_id} #{pane_last} #{pane_current_path}").Output()
		for _, l := range strings.Split(string(out), "\n") {
			if f := strings.SplitN(l, " ", 3); len(f) == 3 && f[0] != self && f[1] == "1" {
				cwd = []byte(f[2])
			}
		}
	}
	if p.ctl != nil {
		c, err := control.Dial(p.ctl.Path)
		if err == nil {
			defer c.Close()
			if _, err := c.Call(control.Request{Cmd: control.CmdOpenPane, Where: "tab", Command: argv, Focus: true,
				Cwd: strings.TrimSpace(string(cwd))}); err == nil {
				return
			}
		}
	}
	p.say("/branch needs germ's window")
}

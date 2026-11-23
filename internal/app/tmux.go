package app

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"

	"github.com/go-gl/glfw/v3.3/glfw"

	"github.com/zach-monroe/germ/internal/term"
)

// tmux control mode: germ runs `tmux -C new-session -A -s NAME` and speaks
// its line protocol. tmux windows become germ tabs and panes become germ
// splits; pane output arrives as %output notifications and keystrokes go
// back with send-keys. tmux keeps the session alive after germ exits.

// tmuxCallback receives a command's output lines. Callbacks marked
// immediate run on the reader goroutine (to stay ordered with %output);
// others run on the main thread.
type tmuxCallback struct {
	fn        func(lines []string, failed bool)
	immediate bool
}

type tmuxEvent struct {
	line   string       // a notification line, or
	cb     tmuxCallback // a command result
	lines  []string
	failed bool
}

type tmuxController struct {
	a       *App
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	session string

	writeMu sync.Mutex // serializes commands and keeps queue order

	mu      sync.Mutex // guards the fields below (shared with the reader)
	queue   []tmuxCallback
	terms   map[int]*term.Terminal // tmux pane ID -> terminal
	pending map[int][]byte         // output for panes not created yet
	events  []tmuxEvent
	done    bool

	// Main-thread state.
	tabs     map[int]*tab  // tmux window ID -> tab
	panes    map[int]*pane // tmux pane ID -> pane
	ready    bool          // initial state received
	lastSize cellRect
	// wantActive remembers a window's active pane when tmux announces it
	// before the pane's layout arrives.
	wantActive map[int]int
	// wantWindow is a window tmux switched to before announcing it
	// (%session-window-changed precedes %window-add), or -1.
	wantWindow int

	// Key bindings (see tmuxkeys.go).
	client        string // this control client's name
	prefixes      []tmuxPrefix
	prefixPending string // prefix key name while waiting for the next key
}

// tmuxBackend sends a pane's input to tmux.
type tmuxBackend struct {
	c   *tmuxController
	id  int // tmux pane ID
	cwd string
}

func (b *tmuxBackend) Write(p []byte)  { b.c.sendKeys(b.id, p) }
func (b *tmuxBackend) Resize(int, int) {}
func (b *tmuxBackend) Close()          { b.c.command(fmt.Sprintf("kill-pane -t %%%d", b.id), nil) }
func (b *tmuxBackend) Cwd() string {
	return b.c.paneCwd(b.id)
}

// startTmux attaches to (or creates) session in control mode.
func (a *App) startTmux(session string) error {
	path, err := exec.LookPath("tmux")
	if err != nil {
		return fmt.Errorf("tmux not found: %w", err)
	}
	var sock []string
	if s := a.cfg.Tmux.Socket; s != "" {
		sock = []string{"-L", s}
	}
	if session == "" {
		session = pickSession(listSessions(path, sock))
	}
	args := append(sock, "-C", "new-session", "-A", "-s", session)
	cmd := exec.Command(path, args...)
	cmd.Env = append(os.Environ(), a.baseEnv...)
	cmd.Env = append(cmd.Env, "TERM_PROGRAM=germ", "COLORTERM=truecolor")
	if a.cfg.Shells.Mode != "off" {
		// Panes inherit the server's environment when germ starts it.
		cmd.Env = append(cmd.Env, shellintEnv([]string{"fish"})...)
		cmd.Env = append(cmd.Env, "GERM_SHELL_INTEGRATION=1")
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start tmux: %w", err)
	}
	c := &tmuxController{
		a: a, cmd: cmd, stdin: stdin, session: session,
		terms: map[int]*term.Terminal{}, pending: map[int][]byte{},
		tabs: map[int]*tab{}, panes: map[int]*pane{}, wantActive: map[int]int{}, wantWindow: -1,
	}
	a.tmux = c
	go c.readLoop(stdout)
	c.queryKeys()
	c.clientSize(a.content())
	c.refresh()
	return nil
}

// listSessions returns the server's sessions and how many clients each
// has attached; none if no server is running.
func listSessions(tmux string, sock []string) map[string]int {
	out, err := exec.Command(tmux, append(sock, "list-sessions", "-F", "#{session_name}\t#{session_attached}")...).Output()
	if err != nil {
		return nil
	}
	m := map[string]int{}
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		name, n, _ := strings.Cut(l, "\t")
		m[name], _ = strconv.Atoi(n)
	}
	return m
}

// pickSession chooses germ's session for a new window: the first germ-N
// session nobody is attached to, else the first unused name. Closing a
// window leaves its session running, so reopening germ picks it back up.
func pickSession(sessions map[string]int) string {
	for i := 1; ; i++ {
		name := "germ"
		if i > 1 {
			name = "germ-" + strconv.Itoa(i)
		}
		if n, ok := sessions[name]; !ok || n == 0 {
			return name
		}
	}
}

// command sends a tmux command; cb (may be nil) gets its output on the
// main thread.
func (c *tmuxController) command(cmd string, cb func([]string, bool)) {
	c.send(cmd, tmuxCallback{fn: cb})
}

func (c *tmuxController) send(cmd string, cb tmuxCallback) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	c.mu.Lock()
	if c.done {
		c.mu.Unlock()
		return
	}
	c.queue = append(c.queue, cb)
	c.mu.Unlock()
	if _, err := io.WriteString(c.stdin, cmd+"\n"); err != nil {
		c.mu.Lock()
		c.done = true
		c.mu.Unlock()
	}
}

// sendKeys forwards input bytes to a pane, as hex so every byte (escape
// sequences included) arrives exactly.
func (c *tmuxController) sendKeys(id int, p []byte) {
	for len(p) > 0 {
		n := min(len(p), 256)
		var b strings.Builder
		fmt.Fprintf(&b, "send-keys -t %%%d -H", id)
		for _, x := range p[:n] {
			fmt.Fprintf(&b, " %02x", x)
		}
		c.command(b.String(), nil)
		p = p[n:]
	}
}

// readLoop parses tmux's output. %output goes straight into terminals
// (they're thread-safe); everything else is queued for the main thread.
func (c *tmuxController) readLoop(r io.Reader) {
	br := bufio.NewReaderSize(r, 1<<16)
	var block []string
	inBlock, ours := false, false
	for {
		line, err := br.ReadString('\n')
		line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		if line != "" || err == nil {
			switch {
			case inBlock && (strings.HasPrefix(line, "%end ") || strings.HasPrefix(line, "%error ")):
				inBlock = false
				if ours {
					c.finish(block, strings.HasPrefix(line, "%error "))
				}
				block = nil
			case inBlock:
				block = append(block, line)
			case strings.HasPrefix(line, "%begin "):
				inBlock, block = true, nil
				// Flags 1 marks a command sent by this client; others
				// (e.g. the initial new-session) have no callback.
				f := strings.Fields(line)
				ours = len(f) >= 4 && f[3] == "1"
			case strings.HasPrefix(line, "%output "):
				c.output(line[len("%output "):])
			case strings.HasPrefix(line, "%extended-output "):
				// %extended-output %ID age ... : value
				rest := line[len("%extended-output "):]
				if i := strings.Index(rest, " : "); i >= 0 {
					id, _, _ := strings.Cut(rest, " ")
					c.output(id + " " + rest[i+3:])
				}
			default:
				c.post(tmuxEvent{line: line})
			}
		}
		if err != nil {
			c.post(tmuxEvent{line: "%exit"})
			return
		}
	}
}

func (c *tmuxController) output(rest string) {
	idStr, data, _ := strings.Cut(rest, " ")
	id, ok := parseTmuxID(idStr)
	if !ok {
		return
	}
	b := unescapeOutput(data)
	c.mu.Lock()
	t := c.terms[id]
	if t == nil {
		if len(c.pending[id]) < 1<<20 {
			c.pending[id] = append(c.pending[id], b...)
		}
		c.mu.Unlock()
		return
	}
	c.mu.Unlock()
	t.Write(b)
	c.a.wake()
}

func (c *tmuxController) finish(lines []string, failed bool) {
	c.mu.Lock()
	var cb tmuxCallback
	if len(c.queue) > 0 {
		cb, c.queue = c.queue[0], c.queue[1:]
	}
	c.mu.Unlock()
	if failed && cb.fn == nil {
		fmt.Fprintln(os.Stderr, "germ: tmux:", strings.Join(lines, " "))
	}
	if cb.fn == nil {
		return
	}
	if cb.immediate {
		cb.fn(lines, failed)
		return
	}
	c.post(tmuxEvent{cb: cb, lines: lines, failed: failed})
}

func (c *tmuxController) post(ev tmuxEvent) {
	c.mu.Lock()
	c.events = append(c.events, ev)
	c.mu.Unlock()
	glfw.PostEmptyEvent()
}

// process handles queued events on the main thread.
func (c *tmuxController) process() {
	c.mu.Lock()
	evs := c.events
	c.events = nil
	c.mu.Unlock()
	for _, ev := range evs {
		if ev.cb.fn != nil {
			ev.cb.fn(ev.lines, ev.failed)
			continue
		}
		c.notification(ev.line)
	}
	// The tab bar takes a row once there are two windows; keep tmux's idea
	// of the client size in step.
	if c.ready && c.a.content() != c.lastSize {
		c.clientSize(c.a.content())
		c.a.relayout()
	}
}

func (c *tmuxController) notification(line string) {
	f := strings.SplitN(line, " ", 5)
	switch f[0] {
	case "%layout-change":
		if len(f) >= 4 {
			if id, ok := parseTmuxID(f[1]); ok {
				c.applyLayout(id, f[2], f[3])
			}
		}
	case "%window-add", "%window-close", "%unlinked-window-close", "%sessions-changed", "%session-changed",
		"%session-renamed":
		c.refresh()
	case "%window-renamed":
		if len(f) >= 3 {
			if id, ok := parseTmuxID(f[1]); ok && c.tabs[id] != nil {
				c.tabs[id].name = strings.Join(f[2:], " ")
				c.a.forceRedraw = true
			}
		}
	case "%session-window-changed":
		if len(f) >= 3 {
			if id, ok := parseTmuxID(f[2]); ok {
				c.showWindow(id)
			}
		}
	case "%pane-mode-changed":
		if len(f) >= 2 {
			c.paneModeChanged(f[1])
		}
	case "%window-pane-changed":
		if len(f) >= 3 {
			wid, ok1 := parseTmuxID(f[1])
			pid, ok2 := parseTmuxID(f[2])
			if ok1 && ok2 {
				c.setActivePane(wid, pid)
			}
		}
	case "%exit":
		c.mu.Lock()
		c.done = true
		c.mu.Unlock()
		c.a.quitting = true
	}
}

// setActivePane focuses tmux pane pid in window wid, or remembers it until
// the pane exists.
func (c *tmuxController) setActivePane(wid, pid int) {
	t, p := c.tabs[wid], c.panes[pid]
	if t == nil || p == nil || t.panes[p.id] == nil {
		c.wantActive[wid] = pid
		return
	}
	delete(c.wantActive, wid)
	if t.active != p {
		old := t.active
		t.active = p
		c.a.focusChanged(old, false)
		c.a.focusChanged(p, true)
		c.a.forceRedraw = true
	}
}

// refresh re-reads all windows and their active panes.
func (c *tmuxController) refresh() {
	c.command(`list-windows -F "#{window_id}	#{window_active}	#{window_name}	#{window_layout}	#{window_visible_layout}	#{window_index}"`,
		func(lines []string, failed bool) {
			if failed {
				return
			}
			seen := map[int]bool{}
			active := -1
			for _, l := range lines {
				f := strings.Split(l, "\t")
				if len(f) < 6 {
					continue
				}
				id, ok := parseTmuxID(f[0])
				if !ok {
					continue
				}
				seen[id] = true
				c.applyLayout(id, f[3], f[4])
				if t := c.tabs[id]; t != nil {
					t.name, t.index = f[2], f[5]
				}
				if f[1] == "1" {
					active = id
				}
			}
			for id, t := range c.tabs {
				if !seen[id] {
					c.removeTab(id, t)
				}
			}
			c.sortTabs()
			// Later active-window changes arrive as notifications; only the
			// first refresh picks the starting tab, so it can't override a
			// switch the user already made.
			if active >= 0 && !c.ready {
				c.showWindow(active)
			}
			c.ready = true
			c.a.forceRedraw = true
		})
	c.command(`display -p "#{session_name}"`, func(lines []string, failed bool) {
		if !failed && len(lines) > 0 && lines[0] != c.session {
			c.session = lines[0] // renamed, or switched with choose-tree etc.
			c.a.forceRedraw = true
		}
	})
	c.command(`list-panes -s -F "#{window_id}	#{pane_id}	#{pane_active}"`, func(lines []string, failed bool) {
		for _, l := range lines {
			f := strings.Split(l, "\t")
			if len(f) < 3 || f[2] != "1" {
				continue
			}
			wid, _ := parseTmuxID(f[0])
			pid, _ := parseTmuxID(f[1])
			c.setActivePane(wid, pid)
		}
		c.a.forceRedraw = true
	})
}

// sortTabs orders tabs by tmux window ID (creation order).
func (c *tmuxController) sortTabs() {
	a := c.a
	cur := a.tab()
	for i := 1; i < len(a.tabs); i++ {
		for j := i; j > 0; j-- {
			x, _ := parseTmuxID(a.tabs[j-1].tmuxID)
			y, _ := parseTmuxID(a.tabs[j].tmuxID)
			if x > y {
				a.tabs[j-1], a.tabs[j] = a.tabs[j], a.tabs[j-1]
			}
		}
	}
	for i, t := range a.tabs {
		if t == cur {
			a.cur = i
		}
	}
}

func (c *tmuxController) showWindow(id int) {
	t := c.tabs[id]
	if t == nil {
		c.wantWindow = id
		return
	}
	c.wantWindow = -1
	for i, x := range c.a.tabs {
		if x == t && i != c.a.cur {
			old := c.a.active()
			c.a.cur = i
			c.a.focusChanged(old, false)
			c.a.focusChanged(t.active, true)
			c.a.forceRedraw = true
		}
	}
}

// applyLayout syncs a tmux window's panes and geometry into its tab.
func (c *tmuxController) applyLayout(wid int, layout, visible string) {
	full, err := parseLayout(layout)
	if err != nil {
		fmt.Fprintln(os.Stderr, "germ:", err)
		return
	}
	vis, err := parseLayout(visible)
	if err != nil {
		vis = full
	}
	a := c.a
	t := c.tabs[wid]
	if t == nil {
		t = &tab{panes: map[int]*pane{}, fixed: true, tmuxID: "@" + strconv.Itoa(wid)}
		c.tabs[wid] = t
		a.tabs = append(a.tabs, t)
		c.sortTabs()
	}

	ids := full.panes(nil)
	want := map[int]bool{}
	for _, id := range ids {
		want[id] = true
	}
	// Drop panes that left this window.
	for _, p := range t.panes {
		if b := p.be.(*tmuxBackend); !want[b.id] {
			c.forget(b.id)
			delete(t.panes, p.id)
			delete(a.panes, p.id)
			if t.active == p {
				t.active = nil
			}
		}
	}
	// Create panes we haven't seen, sized from the full layout.
	var walk func(n *layoutNode)
	walk = func(n *layoutNode) {
		if n.pane >= 0 {
			p := c.panes[n.pane]
			if p == nil {
				p = c.newPane(n.pane, n.rect.W, n.rect.H)
			}
			t.panes[p.id] = p
			p.rect = n.rect
			return
		}
		for _, ch := range n.children {
			walk(ch)
		}
	}
	walk(full)
	// The visible layout differs when a pane is zoomed.
	t.root = vis.toTree(func(tp int) int {
		if p := c.panes[tp]; p != nil {
			p.rect = vis.find(tp)
			return p.id
		}
		return -1
	})
	if pid, ok := c.wantActive[wid]; ok {
		c.setActivePane(wid, pid)
	}
	if t.active == nil {
		for _, p := range t.visible() {
			t.active = p
			break
		}
	}
	for _, p := range t.panes {
		a.resizePane(p, p.rect.W, p.rect.H)
	}
	if c.wantWindow == wid {
		c.showWindow(wid)
	}
	a.forceRedraw = true
}

// find returns the rect of a pane in a layout.
func (n *layoutNode) find(pane int) cellRect {
	if n.pane == pane {
		return n.rect
	}
	for _, c := range n.children {
		if r := c.find(pane); r.W > 0 {
			return r
		}
	}
	return cellRect{}
}

func (c *tmuxController) newPane(id, cols, rows int) *pane {
	a := c.a
	p := a.addPane()
	p.be = &tmuxBackend{c: c, id: id}
	// tmux answers the application's terminal queries itself; replies
	// from germ would be typed into the pane twice.
	p.term = a.newTermNoReplies(p, max(cols, 1), max(rows, 1))
	c.panes[id] = p

	c.mu.Lock()
	c.terms[id] = p.term
	buf := c.pending[id]
	delete(c.pending, id)
	c.mu.Unlock()
	if len(buf) > 0 {
		p.term.Write(buf)
	}
	c.capture(id, p)
	return p
}

// capture loads a pane's existing screen and history. The callbacks run on
// the reader goroutine so they stay ordered with %output.
func (c *tmuxController) capture(id int, p *pane) {
	c.send(fmt.Sprintf("capture-pane -p -e -t %%%d -S -2000", id), tmuxCallback{immediate: true,
		fn: func(lines []string, failed bool) {
			if failed {
				return
			}
			// Trailing blank lines are part of the screen, but writing them
			// would scroll real content away; the cursor fix-up below
			// positions things.
			for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
				lines = lines[:len(lines)-1]
			}
			p.term.Write([]byte("\x1b[H\x1b[2J" + strings.Join(lines, "\r\n")))
		}})
	c.send(fmt.Sprintf(`display -p -t %%%d "#{cursor_x} #{cursor_y}"`, id), tmuxCallback{immediate: true,
		fn: func(lines []string, failed bool) {
			if failed || len(lines) == 0 {
				return
			}
			var x, y int
			if _, err := fmt.Sscanf(lines[0], "%d %d", &x, &y); err == nil {
				p.term.Write([]byte(fmt.Sprintf("\x1b[%d;%dH", y+1, x+1)))
			}
			c.a.wake()
		}})
}

func (c *tmuxController) forget(id int) {
	c.mu.Lock()
	delete(c.terms, id)
	delete(c.pending, id)
	c.mu.Unlock()
	delete(c.panes, id)
}

func (c *tmuxController) removeTab(wid int, t *tab) {
	a := c.a
	for _, p := range t.panes {
		c.forget(p.be.(*tmuxBackend).id)
		delete(a.panes, p.id)
	}
	delete(c.tabs, wid)
	for i, x := range a.tabs {
		if x == t {
			a.tabs = append(a.tabs[:i], a.tabs[i+1:]...)
			if a.cur >= len(a.tabs) {
				a.cur = max(0, len(a.tabs)-1)
			} else if a.cur > i {
				a.cur--
			}
			break
		}
	}
	a.forceRedraw = true
}

// paneCwd is the fallback when the shell hasn't reported its directory;
// returning "" makes new windows and splits use #{pane_current_path}.
func (c *tmuxController) paneCwd(id int) string { return "" }

// ---- actions ----------------------------------------------------------------

func (c *tmuxController) clientSize(r cellRect) {
	c.lastSize = r
	c.command(fmt.Sprintf("refresh-client -C %dx%d", max(r.W, 1), max(r.H, 1)), nil)
}

func (c *tmuxController) selectPane(p *pane) {
	if b, ok := p.be.(*tmuxBackend); ok {
		c.command(fmt.Sprintf("select-pane -t %%%d", b.id), nil)
	}
}

func (c *tmuxController) selectWindow(t *tab) {
	if t.tmuxID != "" {
		c.command("select-window -t "+t.tmuxID, nil)
	}
}

// cwdArg is a -c argument: the known directory, else tmux's own idea of
// the active pane's directory.
func cwdArg(cwd string) string {
	if cwd == "" {
		return ` -c "#{pane_current_path}"`
	}
	return " -c " + tmuxQuote(cwd)
}

func (c *tmuxController) newWindow(cwd string) {
	c.command("new-window"+cwdArg(cwd), nil)
}

func (c *tmuxController) splitPane(p *pane, dir splitDir, cwd string) {
	b, ok := p.be.(*tmuxBackend)
	if !ok {
		return
	}
	flag := "-h"
	if dir == splitV {
		flag = "-v"
	}
	c.command(fmt.Sprintf("split-window %s -t %%%d", flag, b.id)+cwdArg(cwd), nil)
}

func (c *tmuxController) zoomPane(p *pane) {
	if b, ok := p.be.(*tmuxBackend); ok {
		c.command(fmt.Sprintf("resize-pane -Z -t %%%d", b.id), nil)
	}
}

func (c *tmuxController) killWindow(t *tab) {
	c.command("kill-window -t "+t.tmuxID, nil)
}

// detach leaves the session running and stops the client.
func (c *tmuxController) detach() {
	c.command("detach-client", nil)
	c.stdin.Close()
	_ = c.cmd.Wait()
}

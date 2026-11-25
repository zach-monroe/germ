package app

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-gl/glfw/v3.3/glfw"

	"github.com/zach-monroe/germ/internal/control"
)

// The control server: a Unix socket per germ window (see package
// control). Requests are executed on the main thread.

type ctlJob struct {
	req   control.Request
	reply func(*control.Response)
}

type ctlServer struct {
	path string
	ln   net.Listener
	mu   sync.Mutex
	jobs []ctlJob
}

// startControl opens the window's control socket and exports its path to
// panes as GERM_SOCKET. Failure isn't fatal.
func (a *App) startControl() {
	if !a.cfg.Control.Enabled {
		return
	}
	path := control.SocketPath(os.Getpid())
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		fmt.Fprintln(os.Stderr, "germ: control socket:", err)
		return
	}
	os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "germ: control socket:", err)
		return
	}
	os.Chmod(path, 0o600)
	s := &ctlServer{path: path, ln: ln}
	a.ctl = s
	a.baseEnv = append(a.baseEnv, "GERM_SOCKET="+path)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go a.serveControl(conn)
		}
	}()
}

func (a *App) stopControl() {
	if a.ctl != nil {
		a.ctl.ln.Close()
		os.Remove(a.ctl.path)
	}
}

func (a *App) serveControl(conn net.Conn) {
	defer conn.Close()
	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 64*1024), 4<<20)
	enc := json.NewEncoder(conn)
	for sc.Scan() {
		var req control.Request
		var resp *control.Response
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
			resp = ctlErr("bad request: %v", err)
		} else {
			done := make(chan *control.Response, 1)
			a.ctl.mu.Lock()
			a.ctl.jobs = append(a.ctl.jobs, ctlJob{req, func(r *control.Response) { done <- r }})
			a.ctl.mu.Unlock()
			glfw.PostEmptyEvent()
			select {
			case resp = <-done:
			case <-time.After(10 * time.Second):
				resp = ctlErr("timed out")
			}
		}
		if err := enc.Encode(resp); err != nil {
			return
		}
	}
}

func ctlErr(format string, args ...any) *control.Response {
	return &control.Response{Error: fmt.Sprintf(format, args...)}
}

// processControl runs queued requests on the main thread.
func (a *App) processControl() {
	if a.ctl == nil {
		return
	}
	a.ctl.mu.Lock()
	jobs := a.ctl.jobs
	a.ctl.jobs = nil
	a.ctl.mu.Unlock()
	for _, j := range jobs {
		a.handleControl(j.req, j.reply)
	}
}

func (a *App) handleControl(req control.Request, reply func(*control.Response)) {
	ok := func(r *control.Response) {
		r.OK = true
		r.Window = os.Getpid()
		if a.tmux != nil {
			r.Session = a.tmux.session
		}
		reply(r)
	}
	switch req.Cmd {
	case control.CmdSetStatus:
		ttl := time.Duration(req.TTL) * time.Second
		a.setStatus(req.Text, ttl)
		ok(&control.Response{})
		return
	case control.CmdShareUpdate:
		if req.Share == nil {
			reply(ctlErr("share_update needs share"))
			return
		}
		seq, err := a.shareUpdate(req.Share)
		if err != nil {
			reply(ctlErr("%v", err))
			return
		}
		ok(&control.Response{Seq: seq})
		return
	case control.CmdShareWait:
		a.shareWait(func(r *control.Response) {
			r.OK = true
			reply(r)
		})
		return
	case control.CmdListPanes:
		var out []control.PaneInfo
		for _, t := range a.tabs {
			if t.home {
				continue
			}
			for _, n := range t.root.leaves(nil) {
				if p := t.panes[n.id]; p != nil {
					info := a.paneInfo(p)
					if req.History && p.agent.name != "" {
						info.History = p.history()
					}
					out = append(out, info)
				}
			}
		}
		ok(&control.Response{Panes: out})
		return
	}

	p, err := a.resolvePane(req.Pane)
	if err != nil && (req.Cmd != control.CmdOpenPane || req.Pane != "") {
		reply(ctlErr("%v", err))
		return
	}
	switch req.Cmd {
	case control.CmdAgentEvent:
		if err := a.onAgentEvent(p, req.Agent, req.Event, req.Text); err != nil {
			reply(ctlErr("%v", err))
			return
		}
		ok(&control.Response{})
	case control.CmdPaneState:
		info := a.paneInfo(p)
		ok(&control.Response{Pane: &info})
	case control.CmdReadPane:
		info := a.paneInfo(p)
		ok(&control.Response{Pane: &info, Text: readLines(p, req.Lines)})
	case control.CmdSendText:
		if !a.cfg.Control.AllowInput {
			reply(ctlErr("sending input is disabled ([control] allow_input = false)"))
			return
		}
		b := []byte(req.Text)
		if strings.ContainsAny(req.Text, "\r\n") {
			b = encodePasteFor(p, req.Text)
		}
		if req.Enter {
			b = append(b, '\r')
		}
		p.agent.lastInput.Store(time.Now().UnixNano())
		p.send(b)
		ok(&control.Response{})
	case control.CmdClosePane:
		if !a.cfg.Control.AllowInput {
			reply(ctlErr("closing panes is disabled ([control] allow_input = false)"))
			return
		}
		p.be.Close()
		ok(&control.Response{})
	case control.CmdRenameTab:
		a.renameTab(p, req.Text)
		ok(&control.Response{})
	case control.CmdFocusPane:
		a.focusPane(p)
		a.win.Show()
		a.win.Focus() // come to the front (e.g. a click in another window's agents widget)
		ok(&control.Response{})
	case control.CmdOpenPane:
		if !a.cfg.Control.AllowInput {
			reply(ctlErr("opening panes is disabled ([control] allow_input = false)"))
			return
		}
		a.ctlOpen(req, p, ok, reply)
	default:
		reply(ctlErr("unknown command %q", req.Cmd))
	}
}

// resolvePane finds a pane by germ ID ("3") or tmux pane ID ("%5").
func (a *App) resolvePane(ref string) (*pane, error) {
	ref = strings.TrimSpace(ref)
	var p *pane
	if strings.HasPrefix(ref, "%") {
		if a.tmux != nil {
			if id, ok := parseTmuxID(ref); ok {
				p = a.tmux.panes[id]
			}
		}
	} else if id, err := strconv.Atoi(ref); err == nil {
		p = a.panes[id]
	}
	if p == nil {
		if ref == "" {
			return nil, fmt.Errorf("no pane given")
		}
		return nil, fmt.Errorf("no pane %q in this window", ref)
	}
	if _, widget := p.be.(*widgetBackend); widget {
		return nil, fmt.Errorf("pane %q is a home-screen widget", ref)
	}
	return p, nil
}

func (a *App) paneInfo(p *pane) control.PaneInfo {
	now := time.Now()
	info := control.PaneInfo{ID: p.id, Title: p.titleString(), Agent: p.agent.name,
		State: p.state(now).String(), Message: p.agent.message}
	if !p.agent.since.IsZero() && p.agent.name != "" {
		info.StateMs = now.Sub(p.agent.since).Milliseconds()
	}
	if b, ok := p.be.(*tmuxBackend); ok {
		info.Tmux = "%" + strconv.Itoa(b.id)
	}
	for i, t := range a.tabs {
		if t.panes[p.id] != nil {
			info.Tab, info.TabTitle = i, t.title()
			info.Active = t.active == p
			if i == a.cur {
				for _, v := range t.visible() {
					info.Visible = info.Visible || v == p
				}
			}
		}
	}
	if pid := p.be.Pid(); pid > 0 {
		if fg := foregroundPgid(pid); fg > 0 {
			info.Command = procArgs(fg)[0]
		}
	}
	info.Cwd = paneDir(p)
	p.term.Lock()
	info.Cols, info.Rows = p.term.Size()
	p.term.Unlock()
	if out := p.agent.lastOutput.Load(); out > 0 {
		info.IdleMs = now.Sub(time.Unix(0, out)).Milliseconds()
	} else {
		info.IdleMs = -1
	}
	return info
}

// readLines returns the last n lines of a pane (history and screen),
// without trailing blank lines.
func readLines(p *pane, n int) string {
	if n <= 0 {
		n = 50
	}
	n = min(n, 10000)
	p.term.Lock()
	defer p.term.Unlock()
	cols, rows := p.term.Size()
	top := p.term.Pushed()
	// Skip blank rows at the bottom of the screen first, so "the last n
	// lines" means the last n lines with content.
	bottom := top + int64(rows) - 1
	for bottom > top {
		if l := p.term.LineAt(bottom); l != nil && l.Extent() > 0 {
			break
		}
		bottom--
	}
	start := max(bottom-int64(n)+1, top-int64(p.term.ScrollbackLen()))
	return strings.TrimRight(p.term.TextRange(start, 0, bottom, cols), "\n ")
}

// focusPane shows a pane's tab and focuses it.
func (a *App) focusPane(p *pane) {
	for i, t := range a.tabs {
		if t.panes[p.id] != nil {
			a.switchTab(i)
			a.setActive(t, p)
		}
	}
}

// ctlOpen opens a tab or split, optionally running a command. In tmux mode
// the reply waits for tmux to report the new pane.
func (a *App) ctlOpen(req control.Request, target *pane, ok func(*control.Response), reply func(*control.Response)) {
	where := req.Where
	if where == "" {
		where = "tab"
	}
	if where != "tab" && where != "right" && where != "down" {
		reply(ctlErr("where must be tab, right, or down"))
		return
	}
	if target == nil {
		target = a.workPane()
	}
	if target == nil && where != "tab" {
		reply(ctlErr("no pane to split"))
		return
	}
	cwd := req.Cwd
	if cwd == "" && target != nil {
		cwd = target.cwd()
	}

	if a.tmux != nil {
		c := a.tmux
		var cmd string
		if where == "tab" {
			cmd = "new-window -P -F '#{pane_id}'"
		} else {
			flag := "-h"
			if where == "down" {
				flag = "-v"
			}
			cmd = "split-window " + flag + " -P -F '#{pane_id}'"
			if req.Size > 0 && req.Size < 100 {
				cmd += fmt.Sprintf(" -l %d%%", req.Size)
			}
			if b, isTmux := target.be.(*tmuxBackend); isTmux {
				cmd += " -t %" + strconv.Itoa(b.id)
			}
		}
		if !req.Focus {
			cmd += " -d"
		}
		cmd += cwdArg(cwd)
		if len(req.Command) > 0 {
			cmd += " " + tmuxQuote(shellJoin(req.Command))
		}
		c.command(cmd, func(lines []string, failed bool) {
			if failed || len(lines) == 0 {
				reply(ctlErr("tmux: %s", strings.Join(lines, " ")))
				return
			}
			ok(&control.Response{Pane: &control.PaneInfo{Tmux: strings.TrimSpace(lines[0])}})
		})
		return
	}

	argv := req.Command
	switch {
	case len(argv) == 0:
		argv = a.newPaneCommand()
	case len(argv) == 1 && strings.ContainsAny(argv[0], " \t|&;<>()$`'\""):
		argv = []string{"/bin/sh", "-c", argv[0]} // a shell command line
	}
	var p *pane
	if where == "tab" {
		area := a.content()
		np, err := a.newLocalPane(argv, cwd, area.W, area.H)
		if err != nil {
			reply(ctlErr("%v", err))
			return
		}
		p = np
		a.tabs = append(a.tabs, newTab(p))
		a.relayout()
		if req.Focus {
			a.switchTab(len(a.tabs) - 1)
		}
	} else {
		var t *tab
		for _, x := range a.tabs {
			if x.panes[target.id] != nil {
				t = x
			}
		}
		dir := splitH
		if where == "down" {
			dir = splitV
		}
		if p = a.splitLocal(t, target, dir, argv, cwd); p == nil {
			reply(ctlErr("couldn't start %v", argv))
			return
		}
		if req.Size > 0 && req.Size < 100 {
			if n := t.root.find(p.id); n != nil && n.parent != nil {
				n.parent.ratio = 1 - float64(req.Size)/100
				a.relayout()
			}
		}
		if req.Focus {
			a.setActive(t, p)
		}
	}
	info := a.paneInfo(p)
	ok(&control.Response{Pane: &info})
}

// shellJoin quotes argv for sh -c.
func shellJoin(argv []string) string {
	if len(argv) == 1 {
		return argv[0] // a single string is already a shell command
	}
	parts := make([]string, len(argv))
	for i, s := range argv {
		if s != "" && strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_./=:,+@%") == "" {
			parts[i] = s
		} else {
			parts[i] = "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
		}
	}
	return strings.Join(parts, " ")
}

// workPane is the pane commands act on by default: the active pane, or if
// the home tab is showing, the active pane of the first work tab.
func (a *App) workPane() *pane {
	if t := a.tab(); t != nil && !t.home {
		return t.active
	}
	for _, t := range a.tabs {
		if !t.home && t.active != nil {
			return t.active
		}
	}
	return nil
}

// paneDir is a pane's working directory: what its shell reported, else
// its process's directory.
func paneDir(p *pane) string {
	if d := p.cwd(); d != "" {
		return d
	}
	if pid := p.be.Pid(); pid > 0 {
		d, _ := os.Readlink("/proc/" + strconv.Itoa(pid) + "/cwd")
		return d
	}
	return ""
}

// renameTab names the tab holding p (a tmux window name in control mode).
func (a *App) renameTab(p *pane, name string) {
	for _, t := range a.tabs {
		if t.panes[p.id] == nil {
			continue
		}
		if a.tmux != nil && t.tmuxID != "" {
			a.tmux.command("rename-window -t "+t.tmuxID+" "+tmuxQuote(name), nil)
		}
		t.name = name
		a.forceRedraw = true
	}
}

// newAgent opens a tab running `germ agent` (see cmd/germ/agent.go) in the
// current directory: it asks for a task, makes a git worktree, and starts
// the agent there.
// openReview opens a tab running `germ review` for the active pane's
// repository.
func (a *App) openReview() {
	self, err := os.Executable()
	if err != nil {
		return
	}
	cwd := ""
	if p := a.workPane(); p != nil {
		cwd = paneDir(p)
	}
	noop := func(*control.Response) {}
	a.ctlOpen(control.Request{Where: "tab", Command: []string{self, "review"}, Focus: true, Cwd: cwd}, nil, noop, noop)
}

func (a *App) newAgent(task string) {
	self, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, "germ:", err)
		return
	}
	argv := []string{self, "agent"}
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

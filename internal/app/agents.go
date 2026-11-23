package app

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/go-gl/glfw/v3.3/glfw"
)

// Agent attention: germ notices which panes run coding agents (Claude
// Code, Codex, aider, ...) and whether each is working, waiting for you,
// or done, so you can run several at once without checking on each.
//
// Signals:
//   - the pane's foreground process (via /proc), matched against
//     [agents] commands, says whether it's an agent;
//   - notifications (OSC 9/777/99) and bells mean "needs you";
//   - recent output that isn't the echo of your typing means "working",
//     as does an OSC 9;4 progress report;
//   - quiet after working means "done".
//
// Badges clear when you look at the pane.

type agentState uint8

const (
	stateNone    agentState = iota
	stateIdle               // an agent, nothing happening
	stateWorking            // producing output
	stateDone               // stopped working since you last looked
	stateWaiting            // asked for attention
)

func (s agentState) String() string {
	return [...]string{"", "idle", "working", "done", "waiting"}[s]
}

// paneAgent is a pane's attention state. Atomics are written by reader
// goroutines; the rest belongs to the main thread.
type paneAgent struct {
	lastOutput atomic.Int64 // unix nanos
	lastInput  atomic.Int64 // unix nanos
	progress   atomic.Int32 // OSC 9;4 state
	note       atomic.Pointer[agentNote]

	name     string // detected agent command, or ""
	needsYou bool
	message  string // last notification text
	worked   bool   // worked since last viewed
	since    time.Time
}

type agentNote struct {
	title, body string
	bell        bool
}

const (
	workingWindow = 1500 * time.Millisecond // output this recent = working
	echoWindow    = 400 * time.Millisecond  // output this soon after input = echo
)

// state computes a pane's current state.
func (p *pane) state(now time.Time) agentState {
	ag := &p.agent
	if ag.needsYou {
		return stateWaiting
	}
	if ag.name == "" {
		return stateNone
	}
	if p.working(now) {
		return stateWorking
	}
	if ag.worked {
		return stateDone
	}
	return stateIdle
}

func (p *pane) working(now time.Time) bool {
	ag := &p.agent
	switch ag.progress.Load() {
	case 1, 3, 4:
		return true
	}
	out := ag.lastOutput.Load()
	in := ag.lastInput.Load()
	return now.UnixNano()-out < int64(workingWindow) && out-in > int64(echoWindow)
}

// noteOutput records pane output (any goroutine).
func (p *pane) noteOutput() { p.agent.lastOutput.Store(time.Now().UnixNano()) }

// agentTick runs on the main thread about once a second (and on
// notifications): it detects agents, applies notifications, and clears
// badges for panes you're looking at. It reports whether any pane is
// animating (working).
func (a *App) agentTick(detect bool) (animating bool) {
	now := time.Now()
	visible := map[*pane]bool{}
	if t := a.tab(); t != nil && a.focused {
		for _, p := range t.visible() {
			visible[p] = true
		}
	}
	for _, p := range a.panes {
		ag := &p.agent
		if detect {
			if name := a.detectAgent(p); name != ag.name {
				ag.name, ag.worked, ag.since = name, false, now
				a.forceRedraw = true
			}
		}
		if n := ag.note.Swap(nil); n != nil {
			a.onAgentNote(p, n, visible[p])
		}
		if ag.name != "" && p.working(now) {
			if !ag.worked {
				ag.since = now
			}
			ag.worked = true
			animating = true
		}
		if visible[p] {
			if ag.needsYou || ag.worked && !p.working(now) {
				ag.needsYou, ag.worked = false, false
				a.forceRedraw = true
			}
		}
	}
	return animating
}

// onAgentNote handles a notification or bell from p.
func (a *App) onAgentNote(p *pane, n *agentNote, visible bool) {
	ag := &p.agent
	msg := strings.TrimSpace(strings.TrimSpace(n.title + " " + n.body))
	if msg != "" {
		ag.message = msg
	}
	if visible {
		return // you're looking at it
	}
	ag.needsYou = true
	a.forceRedraw = true
	isBell := n.bell
	if isBell && ag.name == "" {
		return // a shell bell just gets a badge
	}
	if !a.cfg.Agents.Notify {
		return
	}
	who := ag.name
	if who == "" {
		who = p.titleString()
	}
	body := msg
	if isBell || body == "" {
		body = "needs your attention"
	}
	if !a.focused {
		a.attention.Store(true)
	}
	go desktopNotify("germ · "+who, body)
}

func desktopNotify(title, body string) {
	if p, err := exec.LookPath("notify-send"); err == nil {
		_ = exec.Command(p, "--app-name=germ", title, body).Run()
	}
}

// detectAgent names the agent running in p's foreground, or "".
func (a *App) detectAgent(p *pane) string {
	pid := p.be.Pid()
	if pid <= 0 {
		return ""
	}
	fg := foregroundPgid(pid)
	if fg <= 0 {
		return ""
	}
	return matchAgent(procArgs(fg), a.cfg.Agents.Commands)
}

// foregroundPgid reads the foreground process group of pid's terminal
// (field 8, tpgid, of /proc/PID/stat).
func foregroundPgid(pid int) int {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0
	}
	// The command name is parenthesized and may contain spaces.
	i := bytes.LastIndexByte(data, ')')
	if i < 0 {
		return 0
	}
	f := strings.Fields(string(data[i+1:]))
	if len(f) < 6 {
		return 0
	}
	v, _ := strconv.Atoi(f[5]) // state ppid pgrp session tty_nr tpgid
	return v
}

// procArgs returns a process's command name and arguments.
func procArgs(pid int) []string {
	base := "/proc/" + strconv.Itoa(pid)
	comm, _ := os.ReadFile(base + "/comm")
	cmdline, _ := os.ReadFile(base + "/cmdline")
	args := []string{strings.TrimSpace(string(comm))}
	for _, s := range strings.Split(strings.TrimRight(string(cmdline), "\x00"), "\x00") {
		if s != "" {
			args = append(args, s)
		}
	}
	return args
}

var interpreters = map[string]bool{"node": true, "bun": true, "deno": true, "python": true,
	"python3": true, "uv": true, "npx": true, "pnpm": true}

// matchAgent checks a process (comm, argv...) against agent names. For
// interpreters (node, python, ...) the script name counts too.
func matchAgent(args []string, names []string) string {
	if len(args) == 0 || len(names) == 0 {
		return ""
	}
	cands := []string{args[0]}
	if len(args) > 1 {
		cands = append(cands, filepath.Base(args[1]))
		if interpreters[filepath.Base(args[1])] || interpreters[args[0]] {
			for _, s := range args[2:] {
				if !strings.HasPrefix(s, "-") {
					cands = append(cands, filepath.Base(s))
					break
				}
			}
		}
	}
	for _, c := range cands {
		c = strings.TrimSuffix(strings.TrimSuffix(c, ".js"), ".py")
		for _, n := range names {
			if c == n {
				return n
			}
		}
	}
	return ""
}

// agentBadge is the tab-bar marker for a tab: the most urgent state of its
// panes.
func (a *App) agentBadge(t *tab, now time.Time) agentState {
	best := stateNone
	for _, p := range t.panes {
		if s := p.state(now); s > best && s != stateIdle {
			best = s
		}
	}
	return best
}

var spinner = []rune("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏")

func badgeRune(s agentState, now time.Time) rune {
	switch s {
	case stateWorking:
		return spinner[now.UnixMilli()/100%int64(len(spinner))]
	case stateDone:
		return '✓'
	case stateWaiting:
		return '●'
	}
	return 0
}

// startAgentTicker wakes the main loop every second for detection.
func (a *App) startAgentTicker() {
	go func() {
		for range time.Tick(time.Second) {
			a.agentPoll.Store(true)
			glfw.PostEmptyEvent()
		}
	}()
}

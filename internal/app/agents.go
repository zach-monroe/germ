package app

import (
	"bytes"
	"fmt"
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
// Agents that report their own state (Claude Code hooks and Codex notify,
// installed by `germ setup agents`, calling `germ ctl agent-event`) override
// the guesses above: see onAgentEvent.
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

	// From the agent's screen (see scanScreen): "esc to interrupt" means
	// working, a permission/question prompt means it's waiting for you.
	screenWorking bool
	screenWaiting bool

	// hook is the state the agent last reported itself (stateNone: it
	// doesn't report, so germ guesses). It holds while the pane's
	// foreground process group is hookPgid.
	hook      agentState
	hookAt    time.Time
	hookPgid  int
	hookAgent string

	// hist is one state sample per second for the last histLen seconds
	// (a ring; histPos is the next slot), for the agents widget timeline.
	hist    [histLen]agentState
	histPos int
	histN   int
}

const histLen = 30 * 60

type agentNote struct {
	title, body string
	bell        bool
	screen      bool // seen on screen rather than sent
}

const (
	workingWindow = 1500 * time.Millisecond // output this recent = working
	echoWindow    = 400 * time.Millisecond  // output this soon after input = echo
	// hookFresh: a reported "working" lapses after this long with neither
	// events nor output (agents don't report being interrupted, but they
	// animate a spinner while they work).
	hookFresh = 15 * time.Second
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
	if ag.hook == stateWaiting {
		return stateWaiting
	}
	if ag.hook == stateNone && ag.screenWaiting && !ag.screenWorking {
		return stateWaiting
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
	switch ag.hook {
	case stateWorking:
		fresh := int64(hookFresh)
		return now.Sub(ag.hookAt) < hookFresh || now.UnixNano()-ag.lastOutput.Load() < fresh
	case stateWaiting, stateDone, stateIdle:
		return false // it told us
	}
	if ag.screenWorking {
		return true
	}
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
	visible := a.visiblePanes()
	for _, p := range a.panes {
		ag := &p.agent
		if detect && ag.hook != stateNone && p.fgPgid() != ag.hookPgid {
			ag.clearHook() // the agent that reported has exited
			a.forceRedraw = true
		}
		if detect {
			name := a.detectAgent(p)
			if name == "" && ag.hook != stateNone {
				name = ag.hookAgent // e.g. run as `node .../claude`
			}
			if name != ag.name {
				ag.name, ag.worked, ag.since = name, false, now
				a.forceRedraw = true
			}
		}
		if n := ag.note.Swap(nil); n != nil && ag.hook == stateNone {
			// (An agent that reports its state through hooks has already
			// told us; its own notifications would be duplicates.)
			a.onAgentNote(p, n, visible[p])
		}
		if detect && ag.name != "" && ag.hook == stateNone {
			wasWaiting := ag.screenWaiting && !ag.screenWorking
			prompt := a.scanScreen(p)
			if ag.screenWaiting && !ag.screenWorking && !wasWaiting {
				// A prompt appeared: like a notification, for agents that
				// don't send one.
				a.onAgentNote(p, &agentNote{body: prompt, screen: true}, visible[p])
			}
		}
		if ag.name != "" && p.working(now) {
			if !ag.worked {
				ag.since = now
			}
			ag.worked = true
			animating = true
		}
		if detect && ag.name != "" {
			ag.hist[ag.histPos] = p.state(now)
			ag.histPos = (ag.histPos + 1) % histLen
			ag.histN = min(ag.histN+1, histLen)
		}
		if visible[p] {
			if ag.needsYou || ag.worked && !p.working(now) {
				ag.needsYou, ag.worked = false, false
				a.forceRedraw = true
			}
			if ag.hook == stateDone {
				ag.hook = stateIdle
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
	if !n.screen {
		ag.needsYou = true // a screen prompt shows as waiting while it lasts
	}
	a.forceRedraw = true
	isBell := n.bell
	if isBell && ag.name == "" {
		return // a shell bell just gets a badge
	}
	body := msg
	if isBell || body == "" {
		body = "needs your attention"
	}
	a.notifyAgent(p, body)
}

// notifyAgent sends a desktop notification about p's agent.
func (a *App) notifyAgent(p *pane, body string) {
	if !a.cfg.Agents.Notify {
		return
	}
	who := p.agent.name
	if who == "" {
		who = p.titleString()
	}
	if !a.focused {
		a.attention.Store(true)
	}
	go desktopNotify("germ · "+who, body)
}

// visiblePanes are the panes you can see now (none while unfocused).
func (a *App) visiblePanes() map[*pane]bool {
	visible := map[*pane]bool{}
	if t := a.tab(); t != nil && a.focused {
		for _, p := range t.visible() {
			visible[p] = true
		}
	}
	return visible
}

func (ag *paneAgent) clearHook() {
	ag.hook, ag.hookAgent, ag.hookPgid = stateNone, "", 0
}

// onAgentEvent applies a state an agent reported about itself (on the
// main thread).
func (a *App) onAgentEvent(p *pane, agent, event, msg string) error {
	ag := &p.agent
	now := time.Now()
	visible := a.visiblePanes()[p]
	msg = strings.TrimSpace(msg)
	if len(msg) > 300 {
		msg = msg[:300] + "…"
	}
	if event == "end" {
		ag.clearHook()
		ag.needsYou, ag.worked = false, false
		if p.be == nil || a.detectAgent(p) == "" {
			ag.name = "" // it was only known from its reports
		}
		a.forceRedraw = true
		return nil
	}
	next := map[string]agentState{"working": stateWorking, "waiting": stateWaiting,
		"done": stateDone, "idle": stateIdle}[event]
	if next == stateNone {
		return fmt.Errorf("unknown agent event %q (want working, waiting, done, idle or end)", event)
	}
	if agent != "" {
		ag.hookAgent = agent
	} else if ag.hookAgent == "" {
		ag.hookAgent = "agent"
	}
	ag.hookPgid = p.fgPgid()
	if ag.name == "" {
		ag.name = ag.hookAgent
	}
	prev := ag.hook
	ag.hook, ag.hookAt = next, now
	if next != prev {
		ag.since = now
		a.forceRedraw = true
	}
	if msg != "" {
		ag.message = msg
	}
	switch next {
	case stateWorking:
		ag.needsYou = false // it got what it was waiting for
		ag.worked = true
		if prev == stateWaiting && msg == "" {
			ag.message = "" // the question was answered
		}
	case stateWaiting:
		if prev != stateWaiting {
			if msg == "" {
				msg = "needs your attention"
			}
			a.onAgentNote(p, &agentNote{body: msg}, visible)
		}
	case stateDone:
		ag.needsYou = false
		if visible {
			ag.worked, ag.hook = false, stateIdle
		} else if prev != stateDone {
			ag.worked = true
			if a.cfg.Agents.NotifyDone {
				body := "finished"
				if msg != "" {
					body = "finished: " + msg
				}
				a.notifyAgent(p, body)
			}
		}
	case stateIdle:
		ag.needsYou = false
	}
	return nil
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

// fgPgid is the foreground process group of the pane's terminal.
func (p *pane) fgPgid() int {
	if p.be == nil {
		return 0
	}
	if pid := p.be.Pid(); pid > 0 {
		return foregroundPgid(pid)
	}
	return 0
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

// scanScreen looks at the bottom of an agent's screen for its working
// indicator and for prompts waiting on you, returning the prompt's line.
func (a *App) scanScreen(p *pane) string {
	ag := &p.agent
	lines := screenTail(p, 12)
	ag.screenWorking, ag.screenWaiting = false, false
	prompt := ""
	for _, l := range lines {
		low := strings.ToLower(l)
		for _, pat := range a.cfg.Agents.WorkingPatterns {
			if pat != "" && strings.Contains(low, strings.ToLower(pat)) {
				ag.screenWorking = true
			}
		}
		for _, pat := range a.cfg.Agents.WaitingPatterns {
			if pat != "" && strings.Contains(low, strings.ToLower(pat)) {
				ag.screenWaiting = true
				// Prefer the question over the options below it.
				if text := strings.TrimSpace(strings.Trim(strings.TrimSpace(l), "│╭╮╰╯─┃")); prompt == "" ||
					strings.Contains(text, "?") && !strings.Contains(prompt, "?") {
					prompt = text
				}
			}
		}
	}
	return prompt
}

// screenTail is the last n non-blank lines of a pane's screen.
func screenTail(p *pane, n int) []string {
	p.term.Lock()
	defer p.term.Unlock()
	cols, rows := p.term.Size()
	top := p.term.Pushed()
	var out []string
	for y := rows - 1; y >= 0 && len(out) < n; y-- {
		l := p.term.TextRange(top+int64(y), 0, top+int64(y), cols)
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}

// history summarizes the last 30 minutes in 10-second buckets, oldest
// first: one character per bucket, the most urgent state in it
// ('!' waiting, 'w' working, 'd' done, 'i' idle, ' ' no agent).
func (p *pane) history() string {
	ag := &p.agent
	const bucket = 10
	out := make([]byte, histLen/bucket)
	for b := range out {
		best := stateNone
		for i := 0; i < bucket; i++ {
			age := histLen - (b*bucket + i) // seconds ago
			if age > ag.histN {
				continue
			}
			s := ag.hist[((ag.histPos-age)%histLen+histLen)%histLen]
			if rank(s) > rank(best) {
				best = s
			}
		}
		out[b] = " iwd!"[map[agentState]int{stateNone: 0, stateIdle: 1, stateWorking: 2, stateDone: 3, stateWaiting: 4}[best]]
	}
	return string(out)
}

// rank orders states by how much they matter in a summary.
func rank(s agentState) int {
	return map[agentState]int{stateNone: 0, stateIdle: 1, stateDone: 2, stateWorking: 3, stateWaiting: 4}[s]
}

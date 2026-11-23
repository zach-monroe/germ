package widgets

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/zach-monroe/germ/internal/control"
)

// agents lists the coding agents running in every germ window, with what
// each is doing: working, waiting for you, or done.
type agents struct {
	mu   sync.Mutex
	rows []agentRow
	at   time.Time
}

type agentRow struct {
	control.PaneInfo
	session string
	window  int
}

func newAgents() Widget { return &agents{} }

func (a *agents) Every() time.Duration { return time.Second }
func (a *agents) Tick() time.Duration  { return 0 }

func (a *agents) Update(context.Context) error {
	var rows []agentRow
	for _, sock := range control.Sockets() {
		c, err := control.Dial(sock)
		if err != nil {
			continue
		}
		resp, err := c.Call(control.Request{Cmd: control.CmdListPanes})
		c.Close()
		if err != nil {
			continue
		}
		for _, p := range resp.Panes {
			if p.Agent != "" {
				rows = append(rows, agentRow{p, resp.Session, resp.Window})
			}
		}
	}
	// Most urgent first: waiting, done, working, idle.
	rank := map[string]int{"waiting": 0, "done": 1, "working": 2, "idle": 3}
	sort.SliceStable(rows, func(i, j int) bool { return rank[rows[i].State] < rank[rows[j].State] })
	a.mu.Lock()
	a.rows, a.at = rows, time.Now()
	a.mu.Unlock()
	return nil
}

var spin = []rune("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏")

func dur(ms int64) string {
	d := time.Duration(ms) * time.Millisecond
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
}

func (a *agents) Draw(w, h int, err error) []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	waiting := 0
	for _, r := range a.rows {
		if r.State == "waiting" {
			waiting++
		}
	}
	info := fmt.Sprintf("%d running", len(a.rows))
	if waiting > 0 {
		info = fmt.Sprintf("%d need you", waiting)
	}
	lines := []string{title(w, "◆", "agents", info), ""}
	if len(a.rows) == 0 {
		lines = append(lines, dim+"  No coding agents running."+reset)
		if h > 6 {
			lines = append(lines, "", dim+"  Start claude, codex, aider… in any tab and"+reset,
				dim+"  their status shows up here."+reset)
		}
		return fill(lines, h, errLine(err, w))
	}
	frame := spin[time.Now().UnixMilli()/1000%int64(len(spin))]
	for _, r := range a.rows {
		var mark, state string
		switch r.State {
		case "working":
			mark, state = cyan+string(frame), cyan+"working "+dur(r.StateMs)
		case "waiting":
			mark, state = yellow+bold+"●", yellow+bold+"needs you"
		case "done":
			mark, state = green+"✓", green+"done"
		default:
			mark, state = dim+"·", dim+"idle"
		}
		where := fmt.Sprintf("tab %d", r.Tab)
		if r.session != "" {
			where = r.session + ":" + where
		}
		dir := filepath.Base(r.Cwd)
		left := fmt.Sprintf(" %s%s %s%-8s%s %s", mark, reset, bold, truncate(r.Agent, 8), reset, truncate(dir, 20))
		right := state + reset + "  " + dim + where + reset
		pad := w - width(left) - width(right) - 1
		lines = append(lines, left+strings.Repeat(" ", max(1, pad))+right)
		if r.State == "waiting" && r.Message != "" && len(lines) < h-1 {
			lines = append(lines, "     "+dim+truncate(r.Message, w-6)+reset)
		}
		if len(lines) >= h-1 {
			break
		}
	}
	return fill(lines, h, errLine(err, w))
}

package app

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Surviving a reboot: tmux keeps sessions while its server runs, but a
// reboot ends it, and every tab, split, directory and agent goes with it.
// germ snapshots its session's layout every 30 seconds; when it later
// creates that session afresh (the server is gone), it rebuilds the tabs
// and splits in the same directories, and types each agent's resume
// command (claude --continue) into its pane without running it.

type sessionSnapshot struct {
	Saved   time.Time        `json:"saved"`
	Windows []windowSnapshot `json:"windows"`
}

type windowSnapshot struct {
	Name   string         `json:"name"`
	Layout string         `json:"layout"`
	Active bool           `json:"active,omitempty"`
	Panes  []paneSnapshot `json:"panes"`
}

type paneSnapshot struct {
	Cwd    string `json:"cwd"`
	Agent  string `json:"agent,omitempty"`
	Active bool   `json:"active,omitempty"`
}

const (
	snapshotEvery  = 30 * time.Second
	snapshotMaxAge = 30 * 24 * time.Hour
)

func snapshotPath(session string) string {
	return filepath.Join(StateDir(), "sessions", session+".json")
}

const snapshotFormat = "#{window_index}\t#{window_name}\t#{window_layout}\t#{window_active}\t#{pane_active}\t#{pane_id}\t#{pane_current_path}"

// saveSnapshot records the session's layout (main thread).
func (c *tmuxController) saveSnapshot() {
	if c.remote != nil || !c.restored || !c.a.cfg.Tmux.Restore || len(c.a.opts.TmuxCommand) > 0 {
		return
	}
	c.command("list-panes -s -t "+tmuxQuote(c.session)+" -F "+tmuxQuote(snapshotFormat), func(lines []string, failed bool) {
		if failed {
			return
		}
		snap := c.parseSnapshot(lines)
		if len(snap.Windows) == 0 {
			return
		}
		data, _ := json.MarshalIndent(snap, "", "  ")
		path := snapshotPath(c.session)
		os.MkdirAll(filepath.Dir(path), 0o755)
		tmp := path + ".tmp"
		if os.WriteFile(tmp, data, 0o644) == nil {
			os.Rename(tmp, path)
		}
	})
}

func (c *tmuxController) parseSnapshot(lines []string) sessionSnapshot {
	snap := sessionSnapshot{Saved: time.Now()}
	byIndex := map[string]int{}
	for _, l := range lines {
		f := strings.SplitN(l, "\t", 7)
		if len(f) < 7 {
			continue
		}
		wi, ok := byIndex[f[0]]
		if !ok {
			wi = len(snap.Windows)
			byIndex[f[0]] = wi
			snap.Windows = append(snap.Windows, windowSnapshot{Name: f[1], Layout: f[2], Active: f[3] == "1"})
		}
		ps := paneSnapshot{Cwd: f[6], Active: f[4] == "1"}
		if id, err := strconv.Atoi(strings.TrimPrefix(f[5], "%")); err == nil {
			if p := c.panes[id]; p != nil {
				ps.Agent = p.agent.name
			}
		}
		snap.Windows[wi].Panes = append(snap.Windows[wi].Panes, ps)
	}
	return snap
}

func loadSnapshot(session string) (*sessionSnapshot, bool) {
	data, err := os.ReadFile(snapshotPath(session))
	if err != nil {
		return nil, false
	}
	var s sessionSnapshot
	if json.Unmarshal(data, &s) != nil || len(s.Windows) == 0 || time.Since(s.Saved) > snapshotMaxAge {
		return nil, false
	}
	return &s, true
}

// restoreSession rebuilds a snapshot in the (new) session: one window per
// saved window, then the original first window is closed.
func (c *tmuxController) restoreSession(s *sessionSnapshot) {
	c.command("display-message -p -t "+tmuxQuote(c.session)+" '#{window_id}'", func(lines []string, failed bool) {
		if failed || len(lines) == 0 {
			c.restored = true
			return
		}
		first := strings.TrimSpace(lines[0])
		for _, w := range s.Windows {
			c.command(restoreWindowCommand(c.session, w), nil)
		}
		c.command("kill-window -t "+tmuxQuote(first), nil)
		for i, w := range s.Windows {
			if w.Active { // windows keep their order; step to it from the first
				cmd := "select-window -t " + tmuxQuote(c.session+":^")
				for range i {
					cmd += " ; next-window -t " + tmuxQuote(c.session)
				}
				c.command(cmd, nil)
			}
		}
		// Type the agents' resume commands once their shells are up
		// (typed earlier, a shell echoes them before its prompt).
		time.AfterFunc(1500*time.Millisecond, func() { c.typeResumes(s) })
		c.restored = true
		c.a.setStatus("restored "+plural(len(s.Windows), "tab")+" from your last session", 8*time.Second)
	})
}

// restoreWindowCommand is one tmux command line that recreates a window:
// its panes in their directories, its layout, and the active pane.
func restoreWindowCommand(session string, w windowSnapshot) string {
	dir := func(p paneSnapshot) string {
		if fi, err := os.Stat(p.Cwd); err == nil && fi.IsDir() {
			return p.Cwd
		}
		home, _ := os.UserHomeDir()
		return home
	}
	var cmds []string
	active := 0
	for i, p := range w.Panes {
		if i == 0 {
			cmds = append(cmds, "new-window -t "+tmuxQuote(session+":")+" -n "+tmuxQuote(w.Name)+" -c "+tmuxQuote(dir(p)))
		} else {
			cmds = append(cmds, "split-window -c "+tmuxQuote(dir(p)))
		}
		if p.Active {
			active = i
		}
	}
	if len(w.Panes) > 1 {
		cmds = append(cmds, "select-layout "+tmuxQuote(w.Layout))
		// The last pane is current; {next} wraps around to the first.
		for range active + 1 {
			cmds = append(cmds, "select-pane -t "+tmuxQuote("{next}"))
		}
	}
	return strings.Join(cmds, " ; ")
}

// resumeCommand is what to type into a restored agent's pane.
func (c *tmuxController) resumeCommand(agent string) string {
	cfg := c.a.cfg.Agents
	if len(cfg.Resume) > 0 && cfg.Resume[0] == agent {
		return shellJoin(cfg.Resume)
	}
	switch agent {
	case "claude":
		return "claude --continue"
	case "codex":
		return "codex resume --last"
	}
	return agent
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return strconv.Itoa(n) + " " + word + "s"
}

// shuttingDown reports whether the machine or the login session is going
// away (so a tmux server that just died was killed, not emptied).
func shuttingDown() bool {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "systemctl", "is-system-running").Output(); err == nil || len(out) > 0 {
		if st := strings.TrimSpace(string(out)); st == "stopping" {
			return true
		}
	}
	if id := os.Getenv("XDG_SESSION_ID"); id != "" {
		if out, err := exec.CommandContext(ctx, "loginctl", "show-session", id, "-p", "State", "--value").Output(); err == nil {
			if strings.TrimSpace(string(out)) == "closing" {
				return true
			}
		}
	}
	return false
}

// typeResumes types each restored agent pane's resume command (not run).
// Windows and panes come back in the snapshot's order.
func (c *tmuxController) typeResumes(s *sessionSnapshot) {
	c.command("list-panes -s -t "+tmuxQuote(c.session)+" -F '#{window_index} #{pane_index} #{pane_id}'", func(lines []string, failed bool) {
		if failed {
			return
		}
		type pos struct{ w, p int }
		var order []pos
		ids := map[pos]string{}
		for _, l := range lines {
			f := strings.Fields(l)
			if len(f) != 3 {
				continue
			}
			w, _ := strconv.Atoi(f[0])
			p, _ := strconv.Atoi(f[1])
			order = append(order, pos{w, p})
			ids[pos{w, p}] = f[2]
		}
		sort.Slice(order, func(i, j int) bool {
			return order[i].w < order[j].w || order[i].w == order[j].w && order[i].p < order[j].p
		})
		// Group by window, in order.
		var windows [][]string
		last := -1 << 31
		for _, o := range order {
			if o.w != last {
				windows = append(windows, nil)
				last = o.w
			}
			windows[len(windows)-1] = append(windows[len(windows)-1], ids[o])
		}
		for wi, w := range s.Windows {
			if wi >= len(windows) || len(windows[wi]) != len(w.Panes) {
				continue // the layout changed since; don't guess
			}
			for pi, p := range w.Panes {
				if p.Agent == "" {
					continue
				}
				if r := c.resumeCommand(p.Agent); r != "" {
					c.command("send-keys -t "+tmuxQuote(windows[wi][pi])+" -l "+tmuxQuote(r), nil)
				}
			}
		}
	})
}

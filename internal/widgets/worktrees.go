package widgets

import (
	"context"
	"fmt"
	"net/url"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zach-monroe/germ/internal/control"
	"github.com/zach-monroe/germ/internal/worktrees"
)

// worktreesW lists the agent worktrees (`germ agent`'s germ/ branches) of
// the repositories open in your tabs, with what each changed and what its
// agent is doing. Click one to go to its agent (or open a tab there).
// Options: repos = ["~/code/app"] (also look in these).
type worktreesW struct {
	extra []string
	panes func() []control.PaneInfo // for tests

	repoOf map[string]string // pane directory -> main checkout ("" = none)

	mu    sync.Mutex
	repos []repoTrees
	ready bool
}

type repoTrees struct {
	repo  string
	items []worktrees.Item
}

func newWorktrees(o Options) Widget {
	w := &worktreesW{panes: allPanes, repoOf: map[string]string{}}
	for _, r := range strings.Split(o.Get("repos", ""), ",") {
		if r = strings.TrimSpace(r); r != "" {
			w.extra = append(w.extra, expandTilde(r))
		}
	}
	return w
}

func (w *worktreesW) Every() time.Duration { return 5 * time.Second }
func (w *worktreesW) Tick() time.Duration  { return 0 }

// allPanes lists the panes of every germ window.
func allPanes() []control.PaneInfo {
	var out []control.PaneInfo
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
			if p.Window == 0 {
				p.Window = resp.Window // an older germ
			}
			out = append(out, p)
		}
	}
	return out
}

func (w *worktreesW) Update(context.Context) error {
	if _, err := exec.LookPath("git"); err != nil {
		return fmt.Errorf("needs git")
	}
	panes := w.panes()
	seen := map[string]bool{}
	var repos []string
	add := func(dir string) {
		repo, ok := w.repoOf[dir]
		if !ok {
			repo, _ = worktrees.MainRepo(dir)
			w.repoOf[dir] = repo
		}
		if repo != "" && !seen[repo] {
			seen[repo] = true
			repos = append(repos, repo)
		}
	}
	for _, d := range w.extra {
		add(d)
	}
	for _, p := range panes {
		if p.Cwd != "" {
			add(p.Cwd)
		}
	}
	var out []repoTrees
	for _, r := range repos {
		if items := worktrees.Load(r, panes); len(items) > 0 {
			out = append(out, repoTrees{r, items})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return filepath.Base(out[i].repo) < filepath.Base(out[j].repo) })
	w.mu.Lock()
	w.repos, w.ready = out, true
	w.mu.Unlock()
	return nil
}

func (w *worktreesW) Draw(cols, h int, err error) []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, waiting := 0, 0
	for _, r := range w.repos {
		n += len(r.items)
		for _, it := range r.items {
			if it.Agent != nil && it.Agent.State == "waiting" {
				waiting++
			}
		}
	}
	info := ""
	switch {
	case waiting > 0:
		info = fmt.Sprintf("%d need you", waiting)
	case n > 0:
		info = fmt.Sprintf("%d open", n)
	}
	lines := []string{title(cols, "⑃", "worktrees", info), ""}
	if !w.ready {
		if err == nil {
			lines = append(lines, center(dim+"looking…"+reset, cols))
		}
		return fill(lines, h, errLine(err, cols))
	}
	if n == 0 {
		lines = append(lines, dim+"  No agent worktrees."+reset)
		if h > 5 {
			lines = append(lines, "", dim+"  Ctrl+Shift+A starts an agent in its own"+reset,
				dim+"  worktree; it shows up here until merged."+reset)
		}
		return fill(lines, h, errLine(err, cols))
	}
	limit := h - boolInt(err != nil)
	for _, r := range w.repos {
		if len(w.repos) > 1 {
			if len(lines) >= limit-1 {
				break
			}
			lines = append(lines, " "+dim+filepath.Base(r.repo)+reset)
		}
		for _, it := range r.items {
			if len(lines) >= limit {
				break
			}
			lines = append(lines, worktreeRow(it, cols))
		}
	}
	return fill(lines, h, errLine(err, cols))
}

// worktreeRow draws "  ✓ fix the parser   +12 −3  2 commits  done".
func worktreeRow(it worktrees.Item, cols int) string {
	mark, state := dim+"·"+reset, ""
	if a := it.Agent; a != nil {
		switch a.State {
		case "working":
			mark, state = cyan+"◐"+reset, cyan+"working"+reset
		case "waiting":
			mark, state = yellow+bold+"●"+reset, yellow+bold+"needs you"+reset
		case "done":
			mark, state = green+"✓"+reset, green+"done"+reset
		default:
			mark, state = dim+"·"+reset, dim+a.Agent+reset
		}
	}
	var parts []string
	if it.Added+it.Removed > 0 {
		parts = append(parts, green+"+"+strconv.Itoa(it.Added)+reset+" "+red+"−"+strconv.Itoa(it.Removed)+reset)
	}
	switch {
	case it.Ahead == 1:
		parts = append(parts, dim+"1 commit"+reset)
	case it.Ahead > 1:
		parts = append(parts, dim+strconv.Itoa(it.Ahead)+" commits"+reset)
	}
	if it.Dirty > 0 {
		parts = append(parts, yellow+"✎"+strconv.Itoa(it.Dirty)+reset)
	}
	if state != "" {
		parts = append(parts, state)
	}
	right := strings.Join(parts, "  ")
	task := it.Task
	if it.Person != "" {
		task = "@" + it.Person + " " + task
	}
	left := "  " + mark + " "
	room := cols - width(left) - width(right) - 2
	if room < 10 {
		right = state
		room = cols - width(left) - width(right) - 2
	}
	row := left + truncate(task, max(0, room))
	if right != "" {
		row += strings.Repeat(" ", max(1, cols-width(row)-width(right)-1)) + right
	}
	return link(worktreeLink(it), row)
}

// worktreeLink goes to the worktree's agent, else anything open there,
// else opens a tab in it.
func worktreeLink(it worktrees.Item) string {
	p := it.Agent
	if p == nil && len(it.Panes) > 0 {
		p = &it.Panes[0]
	}
	if p != nil && p.Window != 0 {
		ref := p.Tmux
		if ref == "" {
			ref = strconv.Itoa(p.ID)
		}
		return fmt.Sprintf("germ-pane:%d/%s", p.Window, ref)
	}
	return "germ-open:" + (&url.URL{Path: it.Dir}).EscapedPath()
}

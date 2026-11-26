// Package worktrees finds the git worktrees `germ agent` makes (branches
// named germ/...) and what each has changed, for `germ review` and the
// worktrees widget.
package worktrees

import (
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/zach-monroe/germ/internal/control"
)

// Item is an agent worktree (a germ/ branch) and its state.
type Item struct {
	Dir, Branch, Task string
	Person            string // a guest's branch (germ agent -for)
	Base, From        string // start commit, branch it came from
	Ahead             int
	Commits           []string // "abc1234 subject", newest first
	Files             []FileStat
	Untracked         []string
	Dirty             int
	Added, Removed    int
	Agent             *control.PaneInfo // the agent's pane, if running
	Panes             []control.PaneInfo
}

// FileStat is one changed file.
type FileStat struct {
	Path           string
	Added, Removed int
}

// MainRepo finds the main checkout of the repository dir is in (also
// from inside one of its worktrees).
func MainRepo(dir string) (string, error) {
	common, err := gitOut(dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", errors.New("not in a git repository")
	}
	if filepath.Base(common) != ".git" {
		return "", errors.New("bare repositories aren't supported")
	}
	return filepath.Dir(common), nil
}

// Load gathers repo's agent worktrees and their state; panes (from
// list_panes) tell which have an agent or anything else open in them.
func Load(repo string, panes []control.PaneInfo) []Item {
	out, _ := gitOut(repo, "worktree", "list", "--porcelain")
	var items []Item
	var cur Item
	flush := func() {
		if strings.HasPrefix(cur.Branch, "germ/") && cur.Dir != "" {
			items = append(items, cur)
		}
		cur = Item{}
	}
	for _, l := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(l, "worktree "):
			flush()
			cur.Dir = strings.TrimPrefix(l, "worktree ")
		case strings.HasPrefix(l, "branch refs/heads/"):
			cur.Branch = strings.TrimPrefix(l, "branch refs/heads/")
		}
	}
	flush()
	for i := range items {
		it := &items[i]
		cfg := func(k string) string { v, _ := gitOut(repo, "config", "branch."+it.Branch+"."+k); return v }
		it.Task, it.Base, it.From = cfg("germ-task"), cfg("germ-base"), cfg("germ-from")
		it.Person = cfg("germ-for")
		if it.From == "" {
			it.From, _ = gitOut(repo, "rev-parse", "--abbrev-ref", "HEAD")
		}
		if it.Base == "" {
			it.Base, _ = gitOut(repo, "merge-base", it.From, it.Branch)
		}
		if it.Task == "" {
			it.Task = strings.ReplaceAll(strings.TrimPrefix(it.Branch, "germ/"), "-", " ")
		}
		if n, err := gitOut(it.Dir, "rev-list", "--count", it.Base+"..HEAD"); err == nil {
			it.Ahead, _ = strconv.Atoi(n)
		}
		if log, _ := gitOut(it.Dir, "log", "--format=%h %s", it.Base+"..HEAD"); log != "" {
			it.Commits = strings.Split(log, "\n")
		}
		if num, _ := gitOut(it.Dir, "diff", "--numstat", it.Base); num != "" {
			for _, l := range strings.Split(num, "\n") {
				f := strings.SplitN(l, "\t", 3)
				if len(f) < 3 {
					continue
				}
				a, _ := strconv.Atoi(f[0]) // "-" for binary files
				r, _ := strconv.Atoi(f[1])
				it.Files = append(it.Files, FileStat{f[2], a, r})
				it.Added += a
				it.Removed += r
			}
		}
		if u, _ := gitOut(it.Dir, "ls-files", "--others", "--exclude-standard"); u != "" {
			it.Untracked = strings.Split(u, "\n")
		}
		if st, _ := gitOut(it.Dir, "status", "--porcelain"); st != "" {
			it.Dirty = len(strings.Split(st, "\n"))
		}
		for _, p := range panes {
			if p.Cwd == it.Dir || strings.HasPrefix(p.Cwd, it.Dir+"/") {
				it.Panes = append(it.Panes, p)
				if p.Agent != "" && it.Agent == nil {
					pp := p
					it.Agent = &pp
				}
			}
		}
	}
	return items
}

func gitOut(dir string, args ...string) (string, error) {
	if dir != "" {
		args = append([]string{"-C", dir}, args...)
	}
	cmd := exec.Command("git", args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return strings.TrimSpace(string(out)), fmt.Errorf("%s", strings.TrimSpace(stderr.String()+" "+err.Error()))
	}
	return strings.TrimSpace(string(out)), nil
}

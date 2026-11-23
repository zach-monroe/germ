package widgets

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// git shows what changed in a repository since a base commit: commits,
// and per-file added/removed lines including uncommitted work. germ agent
// runs it beside each agent. Options: base (commit; default HEAD, i.e.
// uncommitted changes only), dir (default: the working directory).
type gitWidget struct {
	dir, base string

	mu      sync.Mutex
	branch  string
	commits []string
	files   []fileChange
	loaded  bool
}

type fileChange struct {
	path      string
	add, del  int
	binary    bool
	untracked bool
}

func newGit() Widget {
	dir := Option("dir", "")
	if dir == "" {
		dir, _ = os.Getwd()
	}
	if strings.HasPrefix(dir, "~/") {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, dir[2:])
	}
	return &gitWidget{dir: dir, base: Option("base", "HEAD")}
}

func (g *gitWidget) Every() time.Duration { return 2 * time.Second }
func (g *gitWidget) Tick() time.Duration  { return 0 }

func (g *gitWidget) git(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", g.dir}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			return "", fmt.Errorf("%s", strings.TrimSpace(string(ee.Stderr)))
		}
		return "", err
	}
	return string(out), nil
}

func (g *gitWidget) Update(ctx context.Context) error {
	branch, err := g.git(ctx, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return err
	}
	log, _ := g.git(ctx, "log", "--oneline", "--no-decorate", "-n", "50", g.base+"..HEAD")
	num, err := g.git(ctx, "diff", "--numstat", g.base)
	if err != nil {
		return err
	}
	var files []fileChange
	for _, l := range strings.Split(strings.TrimSpace(num), "\n") {
		f := strings.SplitN(l, "\t", 3)
		if len(f) < 3 {
			continue
		}
		fc := fileChange{path: f[2]}
		if f[0] == "-" {
			fc.binary = true
		} else {
			fc.add, _ = strconv.Atoi(f[0])
			fc.del, _ = strconv.Atoi(f[1])
		}
		files = append(files, fc)
	}
	untracked, _ := g.git(ctx, "ls-files", "--others", "--exclude-standard")
	for _, p := range strings.Split(strings.TrimSpace(untracked), "\n") {
		if p != "" {
			files = append(files, fileChange{path: p, untracked: true})
		}
	}
	sort.SliceStable(files, func(i, j int) bool {
		return files[i].add+files[i].del > files[j].add+files[j].del
	})
	var commits []string
	for _, c := range strings.Split(strings.TrimSpace(log), "\n") {
		if c != "" {
			commits = append(commits, c)
		}
	}
	g.mu.Lock()
	g.branch, g.commits, g.files, g.loaded = strings.TrimSpace(branch), commits, files, true
	g.mu.Unlock()
	return nil
}

func (g *gitWidget) Draw(w, h int, err error) []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	info := g.branch
	lines := []string{title(w, "±", "changes", truncate(info, max(0, w-14)))}
	if !g.loaded {
		if err == nil {
			lines = append(lines, "", center(dim+"loading…"+reset, w))
		}
		return fill(lines, h, errLine(err, w))
	}
	adds, dels := 0, 0
	for _, f := range g.files {
		adds += f.add
		dels += f.del
	}
	summary := fmt.Sprintf(" %d files  %s+%d%s %s−%d%s", len(g.files), green, adds, reset, red, dels, reset)
	if len(g.commits) > 0 {
		summary += fmt.Sprintf("  %s%d commits%s", yellow, len(g.commits), reset)
	}
	if len(g.files) == 0 && len(g.commits) == 0 {
		summary = dim + " no changes yet" + reset
	}
	lines = append(lines, summary, "")

	// Files, with a +/- bar scaled to the biggest change.
	most := 1
	for _, f := range g.files {
		most = max(most, f.add+f.del)
	}
	barW := min(12, max(0, w/4))
	for _, f := range g.files {
		if len(lines) >= h-1-min(len(g.commits), 3) {
			break
		}
		var mark, stat string
		switch {
		case f.untracked:
			mark, stat = green+"?"+reset, dim+"new"+reset
		case f.binary:
			mark, stat = yellow+"M"+reset, dim+"bin"+reset
		default:
			mark = yellow + "M" + reset
			n := (f.add + f.del) * barW / most
			a := 0
			if f.add+f.del > 0 {
				a = n * f.add / (f.add + f.del)
			}
			stat = green + strings.Repeat("+", a) + red + strings.Repeat("-", n-a) + reset
		}
		name := truncate(f.path, max(4, w-barW-6))
		pad := w - 4 - width(name) - width(stat)
		lines = append(lines, " "+mark+" "+name+strings.Repeat(" ", max(1, pad))+stat)
	}
	if len(g.commits) > 0 && len(lines) < h-2 {
		lines = append(lines, "")
		for _, c := range g.commits {
			if len(lines) >= h-1 {
				break
			}
			sha, msg, _ := strings.Cut(c, " ")
			lines = append(lines, " "+yellow+sha+reset+" "+truncate(msg, max(4, w-len(sha)-3)))
		}
	}
	return fill(lines, h, errLine(err, w))
}

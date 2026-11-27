package worktrees

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Git runs git in dir ("" = the current directory) and returns its
// trimmed output; errors carry git's stderr.
func Git(dir string, args ...string) (string, error) {
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

var slugRE = regexp.MustCompile(`[^a-z0-9]+`)

// Slug makes a branch-friendly name from a task.
func Slug(task string) string {
	s := strings.Trim(slugRE.ReplaceAllString(strings.ToLower(task), "-"), "-")
	var out []string
	n := 0
	for _, w := range strings.Split(s, "-") {
		if n+len(w) > 32 || len(out) == 6 {
			break
		}
		out = append(out, w)
		n += len(w) + 1
	}
	s = strings.Join(out, "-")
	if s == "" {
		s = "agent-" + time.Now().Format("0102-1504")
	}
	return s
}

// Root is the directory repo's agent worktrees go in: configured ("{repo}"
// is the repository's name, "~/" the home directory), or REPO.worktrees
// beside it.
func Root(repo, configured string) string {
	if configured == "" {
		return repo + ".worktrees"
	}
	root := strings.ReplaceAll(configured, "{repo}", filepath.Base(repo))
	if strings.HasPrefix(root, "~/") {
		home, _ := os.UserHomeDir()
		root = filepath.Join(home, root[2:])
	}
	return root
}

// Create makes a worktree on a new germ/NAME branch (NAME-2, -3, ... if
// taken) from repo's HEAD, under root, and records the task, the start
// commit, the branch it came from and the person it's for (if any) in
// the branch's git config, where Load finds them. The returned Item has
// Dir, Branch, Task, Person, Base and From set.
func Create(repo, root, name, task, person string) (Item, error) {
	it := Item{Task: task, Person: person}
	it.Base, _ = Git(repo, "rev-parse", "HEAD")
	it.From, _ = Git(repo, "rev-parse", "--abbrev-ref", "HEAD")
	for i := 1; ; i++ {
		cand := name
		if i > 1 {
			cand = fmt.Sprintf("%s-%d", name, i)
		}
		_, branchErr := Git(repo, "rev-parse", "--verify", "--quiet", "refs/heads/germ/"+cand)
		if _, statErr := os.Stat(filepath.Join(root, cand)); branchErr != nil && errors.Is(statErr, os.ErrNotExist) {
			name = cand
			break
		}
	}
	it.Branch = "germ/" + name
	it.Dir = filepath.Join(root, name)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return Item{}, err
	}
	if _, err := Git(repo, "worktree", "add", "-b", it.Branch, it.Dir, it.Base); err != nil {
		return Item{}, fmt.Errorf("git worktree add: %v", err)
	}
	set := func(k, v string) { Git(repo, "config", "branch."+it.Branch+"."+k, v) }
	set("germ-task", task)
	set("germ-base", it.Base)
	set("germ-from", it.From)
	if person != "" {
		set("germ-for", person)
	}
	return it, nil
}

// Commit stages everything in dir, untracked files included, and commits.
func Commit(dir, msg string) error {
	if _, err := Git(dir, "add", "-A"); err != nil {
		return err
	}
	if out, err := Git(dir, "commit", "-q", "-m", msg); err != nil {
		return fmt.Errorf("commit failed: %v %s", err, out)
	}
	return nil
}

// Merge merges an agent's branch into into (which must be checked out,
// and clean, in the main repository) with a merge commit.
func Merge(repo, branch, into, task string) error {
	if dirty, _ := Git(repo, "status", "--porcelain", "--untracked-files=no"); dirty != "" {
		return fmt.Errorf("%s has uncommitted changes", repo)
	}
	if cur, _ := Git(repo, "rev-parse", "--abbrev-ref", "HEAD"); cur != into {
		return fmt.Errorf("%s is now on %s, not %s", repo, cur, into)
	}
	msg := "Merge " + branch
	if task != "" {
		msg += ": " + task
	}
	if out, err := Git(repo, "merge", "--no-ff", "-m", msg, branch); err != nil {
		Git(repo, "merge", "--abort")
		return fmt.Errorf("%v %s", err, out)
	}
	return nil
}

// Remove deletes an agent's worktree, its branch and the branch's config
// (force: even with unmerged or uncommitted work).
func Remove(repo, dir, branch string, force bool) error {
	args := []string{"worktree", "remove", dir}
	del := "-d"
	if force {
		args = []string{"worktree", "remove", "--force", dir}
		del = "-D"
	}
	if _, err := Git(repo, args...); err != nil {
		return err
	}
	if _, err := Git(repo, "branch", del, branch); err != nil {
		return err
	}
	Git(repo, "config", "--remove-section", "branch."+branch)
	return nil
}

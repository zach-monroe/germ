package worktrees

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zach-monroe/germ/internal/control"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestLoadMergeRemove(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	for _, k := range []string{"AUTHOR", "COMMITTER"} {
		t.Setenv("GIT_"+k+"_NAME", "t")
		t.Setenv("GIT_"+k+"_EMAIL", "t@t")
	}
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	os.Mkdir(repo, 0o755)
	git(t, repo, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(repo, "a.go"), []byte("package a\n"), 0o644)
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-q", "-m", "init")
	base := git(t, repo, "rev-parse", "HEAD")

	mk := func(name, task string) string {
		dir := filepath.Join(root, "wt", name)
		git(t, repo, "worktree", "add", "-q", "-b", "germ/"+name, dir, base)
		git(t, repo, "config", "branch.germ/"+name+".germ-task", task)
		git(t, repo, "config", "branch.germ/"+name+".germ-base", base)
		git(t, repo, "config", "branch.germ/"+name+".germ-from", "main")
		return dir
	}
	fix := mk("fix-parser", "fix the parser")
	os.WriteFile(filepath.Join(fix, "a.go"), []byte("package a\n\nfunc A() {}\n"), 0o644)
	git(t, fix, "commit", "-q", "-am", "add A")
	os.WriteFile(filepath.Join(fix, "b.go"), []byte("package a\n"), 0o644) // untracked
	junk := mk("try-thing", "")

	// Not an agent worktree: ignored.
	git(t, repo, "worktree", "add", "-q", "-b", "feature", filepath.Join(root, "wt", "feature"), base)

	if got, err := MainRepo(fix); err != nil || got != repo {
		t.Fatalf("mainRepo from worktree: %q %v", got, err)
	}

	panes := []control.PaneInfo{{ID: 3, Cwd: fix, Agent: "claude", State: "done"}, {ID: 4, Cwd: fix}}
	items := Load(repo, panes)
	if len(items) != 2 {
		t.Fatalf("got %d items", len(items))
	}
	it := items[0]
	if it.Task != "fix the parser" || it.Ahead != 1 || it.Added != 2 || len(it.Untracked) != 1 || it.Dirty != 1 {
		t.Errorf("fix item: %+v", it)
	}
	if it.Agent == nil || it.Agent.ID != 3 || len(it.Panes) != 2 {
		t.Errorf("panes: agent %+v, %d panes", it.Agent, len(it.Panes))
	}
	if items[1].Task != "try thing" || items[1].Agent != nil {
		t.Errorf("junk item: %+v", items[1])
	}

	// Merge: commit the untracked file, merge, remove the worktree.
	if err := Commit(it.Dir, it.Task); err != nil {
		t.Fatal(err)
	}
	if err := Merge(repo, it.Branch, it.From, it.Task); err != nil {
		t.Fatal(err)
	}
	if err := Remove(repo, it.Dir, it.Branch, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(repo, "b.go")); err != nil {
		t.Error("merge didn't bring the new file")
	}
	if !strings.Contains(git(t, repo, "log", "-1", "--format=%s"), "fix the parser") {
		t.Error("merge message lacks the task")
	}
	if out, _ := exec.Command("git", "-C", repo, "config", "--get-regexp", "branch.germ/fix-parser").Output(); len(out) > 0 {
		t.Errorf("branch config left behind: %s", out)
	}

	if err := Remove(repo, junk, "germ/try-thing", true); err != nil {
		t.Fatal(err)
	}
	if items := Load(repo, nil); len(items) != 0 {
		t.Errorf("%d items left", len(items))
	}
}

func TestCreate(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	for _, k := range []string{"AUTHOR", "COMMITTER"} {
		t.Setenv("GIT_"+k+"_NAME", "t")
		t.Setenv("GIT_"+k+"_EMAIL", "t@t")
	}
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	os.Mkdir(repo, 0o755)
	git(t, repo, "init", "-q", "-b", "main")
	git(t, repo, "commit", "-q", "--allow-empty", "-m", "init")
	wt := Root(repo, "")
	a, err := Create(repo, wt, "fix", "fix it", "")
	if err != nil {
		t.Fatal(err)
	}
	b, err := Create(repo, wt, "fix", "fix it again", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if a.Branch != "germ/fix" || b.Branch != "germ/fix-2" || b.Dir != filepath.Join(repo+".worktrees", "fix-2") || a.From != "main" {
		t.Errorf("a %+v b %+v", a, b)
	}
	items := Load(repo, nil)
	if len(items) != 2 || items[1].Task != "fix it again" || items[1].Person != "alice" || items[1].Base != a.Base {
		t.Errorf("loaded %+v", items)
	}
	if got := Root("/src/germ", "~/wt/{repo}"); !strings.HasSuffix(got, "/wt/germ") || strings.HasPrefix(got, "~") {
		t.Errorf("Root = %q", got)
	}
}

func TestSlug(t *testing.T) {
	if s := Slug("Fix the login bug in auth/session.go!"); s != "fix-the-login-bug-in-auth" {
		t.Errorf("slug = %q", s)
	}
	if s := Slug("   "); !strings.HasPrefix(s, "agent-") {
		t.Errorf("empty slug = %q", s)
	}
}

package widgets

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zach-monroe/germ/internal/control"
)

func gitT(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
}

func TestWorktreesWidget(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	for _, k := range []string{"AUTHOR", "COMMITTER"} {
		t.Setenv("GIT_"+k+"_NAME", "t")
		t.Setenv("GIT_"+k+"_EMAIL", "t@t")
	}
	root := t.TempDir()
	repo := filepath.Join(root, "app")
	os.Mkdir(repo, 0o755)
	gitT(t, repo, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(repo, "a.txt"), []byte("one\n"), 0o644)
	gitT(t, repo, "add", ".")
	gitT(t, repo, "commit", "-q", "-m", "init")
	busy := filepath.Join(root, "app.worktrees", "fix-login")
	idle := filepath.Join(root, "app.worktrees", "old idea")
	gitT(t, repo, "worktree", "add", "-q", "-b", "germ/fix-login", busy)
	gitT(t, repo, "config", "branch.germ/fix-login.germ-task", "fix the login flow")
	gitT(t, repo, "worktree", "add", "-q", "-b", "germ/old-idea", idle)
	os.WriteFile(filepath.Join(busy, "a.txt"), []byte("one\ntwo\nthree\n"), 0o644)
	gitT(t, busy, "commit", "-q", "-am", "more")

	panes := []control.PaneInfo{
		{ID: 1, Cwd: repo, Window: 77}, // the main checkout, open in a tab
		{ID: 5, Tmux: "%9", Cwd: busy, Agent: "claude", State: "waiting", Window: 77}, // an agent in the worktree
		{ID: 6, Cwd: "/", Window: 77}, // not a repo
	}
	w := &worktreesW{panes: func() []control.PaneInfo { return panes }, repoOf: map[string]string{}}
	if err := w.Update(context.Background()); err != nil {
		t.Fatal(err)
	}
	out := strings.Join(w.Draw(70, 8, nil), "\n")
	for _, want := range []string{"1 need you", "fix the login flow", "+2", "1 commit", "needs you",
		"germ-pane:77/%9", "old idea", "germ-open:" + filepath.Join(root, "app.worktrees", "old%20idea")} {
		if !strings.Contains(out, want) {
			t.Errorf("draw lacks %q:\n%s", want, out)
		}
	}
	for _, sz := range [][2]int{{70, 8}, {30, 3}, {18, 4}} {
		lines := w.Draw(sz[0], sz[1], nil)
		if len(lines) > sz[1] {
			t.Errorf("%v: %d lines", sz, len(lines))
		}
		for _, l := range lines {
			if width(l) > sz[0] {
				t.Errorf("%v: too wide (%d): %q", sz, width(l), l)
			}
		}
	}

	// Nothing open in that repo any more: it drops out (unless in repos).
	panes = panes[2:]
	w.Update(context.Background())
	if out := strings.Join(w.Draw(70, 8, nil), "\n"); !strings.Contains(out, "No agent worktrees") {
		t.Errorf("after closing the tabs:\n%s", out)
	}
	w.extra = []string{repo}
	w.Update(context.Background())
	if out := strings.Join(w.Draw(70, 8, nil), "\n"); !strings.Contains(out, "fix the login flow") {
		t.Errorf("with repos option:\n%s", out)
	}
}

package app

import (
	"os"
	"slices"
	"strings"
	"testing"
)

func TestRestoreWindowCommand(t *testing.T) {
	dir := t.TempDir()
	w := windowSnapshot{Name: "my tab", Layout: "abcd,80x24,0,0{40x24,0,0,1,39x24,41,0,2}", Panes: []paneSnapshot{
		{Cwd: dir}, {Cwd: "/no/such/dir", Agent: "claude", Active: true},
	}}
	cmd := restoreWindowCommand("germ", w)
	home, _ := os.UserHomeDir()
	for _, want := range []string{
		"new-window -t 'germ:' -n 'my tab' -c '" + dir + "'",
		"split-window -c '" + home + "'", // missing directories fall back to home
		"select-layout 'abcd,80x24,0,0{40x24,0,0,1,39x24,41,0,2}'",
	} {
		if !strings.Contains(cmd, want) {
			t.Errorf("missing %q in:\n%s", want, cmd)
		}
	}
	// The second pane is active: from the last pane, {next} twice.
	if n := strings.Count(cmd, "select-pane -t '{next}'"); n != 2 {
		t.Errorf("select-pane {next} x%d:\n%s", n, cmd)
	}
	single := restoreWindowCommand("germ", windowSnapshot{Name: "x", Panes: []paneSnapshot{{Cwd: dir}}})
	if strings.Contains(single, "select-layout") || strings.Contains(single, "split-window") {
		t.Errorf("single pane: %s", single)
	}
}

func TestResumeCommand(t *testing.T) {
	a := &App{}
	a.cfg.Agents.Resume = []string{"claude", "--continue"}
	c := &tmuxController{a: a}
	if got := c.resumeCommand("claude"); got != "claude --continue" {
		t.Errorf("claude: %q", got)
	}
	if got := c.resumeCommand("aider"); got != "aider" {
		t.Errorf("aider: %q", got)
	}
}

func TestSortTabsByWindowIndex(t *testing.T) {
	home := &tab{home: true}
	mk := func(id, index string) *tab { return &tab{tmuxID: id, index: index} }
	// @12 was made first but sits at index 1: tab 0 was closed and a new
	// window (@16) took the free index. A window germ hasn't seen the
	// index of yet goes last.
	w12, w16, w17, w20 := mk("@12", "1"), mk("@16", "0"), mk("@17", "2"), mk("@20", "")
	a := &App{tabs: []*tab{home, w12, w20, w16, w17}, cur: 1}
	(&tmuxController{a: a}).sortTabs()
	if want := []*tab{home, w16, w12, w17, w20}; !slices.Equal(a.tabs, want) {
		var got []string
		for _, x := range a.tabs {
			got = append(got, x.tmuxID)
		}
		t.Errorf("order %v", got)
	}
	if a.tabs[a.cur] != w12 {
		t.Error("the current tab changed")
	}
}

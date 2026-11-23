package app

import (
	"os"
	"testing"
	"time"
)

func TestMatchAgent(t *testing.T) {
	names := []string{"claude", "codex", "gemini", "aider"}
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"claude", "/opt/claude-code/bin/claude"}, "claude"},
		{[]string{"node", "node", "/usr/lib/node_modules/@google/gemini-cli/dist/gemini.js"}, "gemini"},
		{[]string{"MainThread", "/usr/bin/python3", "-m", "aider"}, "aider"},
		{[]string{"zsh", "-zsh"}, ""},
		{[]string{"node", "node", "server.js"}, ""},
	} {
		if got := matchAgent(c.args, names); got != c.want {
			t.Errorf("%v: got %q, want %q", c.args, got, c.want)
		}
	}
}

func TestForegroundPgid(t *testing.T) {
	// Tests may run without a controlling terminal (tpgid -1); just make
	// sure our own stat line parses.
	if v := foregroundPgid(os.Getpid()); v == 0 {
		t.Errorf("couldn't parse /proc/self/stat")
	}
}

func TestPaneState(t *testing.T) {
	p := &pane{}
	now := time.Now()
	if p.state(now) != stateNone {
		t.Error("plain pane has a state")
	}
	p.agent.name = "claude"
	if p.state(now) != stateIdle {
		t.Error("want idle")
	}
	// Echo of typing isn't work.
	p.agent.lastInput.Store(now.UnixNano())
	p.agent.lastOutput.Store(now.Add(100 * time.Millisecond).UnixNano())
	if p.state(now.Add(200*time.Millisecond)) != stateIdle {
		t.Error("echo counted as work")
	}
	p.agent.lastOutput.Store(now.Add(time.Second).UnixNano())
	if p.state(now.Add(time.Second)) != stateWorking {
		t.Error("want working")
	}
	p.agent.worked = true
	if p.state(now.Add(10*time.Second)) != stateDone {
		t.Error("want done")
	}
	p.agent.needsYou = true
	if p.state(now) != stateWaiting {
		t.Error("want waiting")
	}
}

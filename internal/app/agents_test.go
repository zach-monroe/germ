package app

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/zach-monroe/germ/internal/config"
	"github.com/zach-monroe/germ/internal/term"
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

func TestScanScreen(t *testing.T) {
	a := &App{cfg: config.Default()}
	p := &pane{term: term.New(60, 10, term.Options{})}
	p.agent.name = "claude"
	p.term.Write([]byte("> fix the bug\r\n\r\n✻ Thinking… (3s · esc to interrupt)\r\n"))
	a.scanScreen(p)
	if !p.agent.screenWorking || p.agent.screenWaiting {
		t.Errorf("working screen: working %v waiting %v", p.agent.screenWorking, p.agent.screenWaiting)
	}
	if p.state(time.Now()) != stateWorking {
		t.Error("want working")
	}
	p.term.Write([]byte("\x1b[2J\x1b[H╭──╮\r\n│ Do you want to make this edit to main.go? │\r\n│ ❯ 1. Yes │\r\n"))
	prompt := a.scanScreen(p)
	if p.agent.screenWorking || !p.agent.screenWaiting || prompt != "Do you want to make this edit to main.go?" {
		t.Errorf("prompt %q, working %v waiting %v", prompt, p.agent.screenWorking, p.agent.screenWaiting)
	}
	if p.state(time.Now()) != stateWaiting {
		t.Error("want waiting")
	}
}

func TestHistory(t *testing.T) {
	p := &pane{}
	ag := &p.agent
	push := func(s agentState, n int) {
		for i := 0; i < n; i++ {
			ag.hist[ag.histPos] = s
			ag.histPos = (ag.histPos + 1) % histLen
			ag.histN = min(ag.histN+1, histLen)
		}
	}
	push(stateWorking, 25)
	push(stateWaiting, 1)
	push(stateIdle, 4) // 30 s of samples
	h := p.history()
	if len(h) != 180 || strings.TrimLeft(h, " ") != "ww!" { // 10-second buckets, most urgent wins
		t.Errorf("history tail %q", strings.TrimLeft(h, " "))
	}
}

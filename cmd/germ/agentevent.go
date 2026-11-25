package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/zach-monroe/germ/internal/control"
)

// `germ ctl agent-event` lets a coding agent tell germ what it's doing, so
// tab badges, notifications and `wait_for` don't have to guess from the
// screen. `germ setup agents` wires it into Claude Code and Codex.
//
//	germ ctl agent-event claude          a Claude Code hook (JSON on stdin)
//	germ ctl agent-event codex JSON      Codex's notify program
//	germ ctl agent-event [-agent NAME] [-message TEXT] working|waiting|done|idle|end
//
// It's called from inside agents, often several times a minute, so it never
// fails loudly: outside germ it does nothing, it gives up after two seconds,
// it prints nothing on stdout (Claude Code would read it as a hook
// decision) and always exits 0 (a Stop hook exiting 2 keeps Claude going).
// Set GERM_AGENT_EVENT_DEBUG=1 to see errors on stderr.

func runAgentEvent(args []string) int {
	debug := os.Getenv("GERM_AGENT_EVENT_DEBUG") != ""
	logf := func(format string, a ...any) {
		if debug {
			fmt.Fprintf(os.Stderr, "germ agent-event: "+format+"\n", a...)
		}
	}
	time.AfterFunc(2*time.Second, func() { logf("timed out"); os.Exit(0) })

	fs := flag.NewFlagSet("germ ctl agent-event", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	agent := fs.String("agent", "", "agent name")
	message := fs.String("message", "", "message to show")
	if err := fs.Parse(args); err != nil || fs.NArg() == 0 {
		logf("usage: germ ctl agent-event [-agent NAME] [-message TEXT] working|waiting|done|idle|end")
		return 0
	}
	var ev agentEvent
	switch fs.Arg(0) {
	case "claude":
		ev = claudeEvent(readStdin())
	case "codex":
		ev = codexEvent(strings.Join(fs.Args()[1:], " "))
	default:
		ev = agentEvent{agent: *agent, event: fs.Arg(0), message: *message}
	}
	if ev.event == "" {
		logf("nothing to report")
		return 0
	}
	if *agent != "" {
		ev.agent = *agent
	}
	// Only report to the germ window this agent runs in.
	if os.Getenv("GERM_SOCKET") == "" {
		logf("not running in germ")
		return 0
	}
	c, err := control.Connect()
	if err != nil {
		logf("%v", err)
		return 0
	}
	defer c.Close()
	self := c.Self()
	if self == "" {
		logf("not in a pane of a running germ window")
		return 0
	}
	if _, err := c.Call(control.Request{Cmd: control.CmdAgentEvent, Pane: self,
		Agent: ev.agent, Event: ev.event, Text: ev.message}); err != nil {
		logf("%v", err)
	}
	return 0
}

type agentEvent struct{ agent, event, message string }

// readStdin reads a hook's JSON input, if any is piped in.
func readStdin() []byte {
	if fi, err := os.Stdin.Stat(); err != nil || fi.Mode()&os.ModeCharDevice != 0 {
		return nil
	}
	b, _ := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
	return b
}

// claudeEvent maps a Claude Code hook's input to a state.
// https://code.claude.com/docs/en/hooks
func claudeEvent(input []byte) agentEvent {
	var in struct {
		Event            string `json:"hook_event_name"`
		NotificationType string `json:"notification_type"`
		Message          string `json:"message"`
		ToolName         string `json:"tool_name"`
		LastMessage      string `json:"last_assistant_message"`
		Reason           string `json:"reason"`
		AgentID          string `json:"agent_id"`
	}
	if json.Unmarshal(input, &in) != nil {
		return agentEvent{}
	}
	ev := agentEvent{agent: "claude"}
	if in.AgentID != "" {
		return agentEvent{} // a subagent's hook: the main agent's state is what matters
	}
	switch in.Event {
	case "UserPromptSubmit", "PreToolUse", "PostToolUse", "PostToolUseFailure", "PreCompact", "SubagentStop":
		ev.event = "working"
	case "PermissionRequest":
		ev.event = "waiting"
		ev.message = "wants to use " + in.ToolName
		if in.ToolName == "" {
			ev.message = "needs your permission"
		}
	case "Notification":
		switch in.NotificationType {
		case "permission_prompt", "elicitation_dialog", "elicitation_url_dialog", "agent_needs_input":
			ev.event, ev.message = "waiting", in.Message
		case "":
			// Older versions don't say; the idle reminder isn't a question.
			if strings.Contains(strings.ToLower(in.Message), "waiting for your input") {
				return agentEvent{}
			}
			ev.event, ev.message = "waiting", in.Message
		default:
			return agentEvent{} // idle reminders, auth, quota notices
		}
	case "Stop":
		ev.event, ev.message = "done", firstLine(in.LastMessage, 120)
	case "StopFailure":
		ev.event, ev.message = "waiting", "stopped with an error"
	case "SessionStart":
		ev.event = "idle"
	case "SessionEnd":
		ev.event = "end"
	default:
		return agentEvent{}
	}
	return ev
}

// codexEvent maps Codex's notify payload (passed as an argument).
func codexEvent(arg string) agentEvent {
	var in struct {
		Type    string `json:"type"`
		Message string `json:"last-assistant-message"`
	}
	if json.Unmarshal([]byte(arg), &in) != nil || in.Type != "agent-turn-complete" {
		return agentEvent{}
	}
	return agentEvent{agent: "codex", event: "done", message: firstLine(in.Message, 120)}
}

func firstLine(s string, n int) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	if r := []rune(s); len(r) > n {
		s = string(r[:n-1]) + "…"
	}
	return s
}

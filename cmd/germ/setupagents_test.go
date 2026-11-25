package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const userSettings = `{
  "model": "opus",
  "permissions": {"allow": ["Bash(go test:*)"]},
  "hooks": {
    "PostToolUse": [
      {"matcher": "Edit", "hooks": [{"type": "command", "command": "gofmt -w"}]}
    ]
  },
  "statusLine": {"type": "command", "command": "~/bin/status"}
}`

func TestEditClaudeSettings(t *testing.T) {
	const cmd = "/usr/bin/germ ctl agent-event claude"
	out, changed, err := editClaudeSettings([]byte(userSettings), cmd, false)
	if err != nil || !changed {
		t.Fatalf("changed %v err %v", changed, err)
	}
	s := string(out)
	// The user's settings survive, in their order.
	for _, want := range []string{`"model"`, `"permissions"`, `"hooks"`, `"statusLine"`} {
		if !strings.Contains(s, want) {
			t.Fatalf("lost %s:\n%s", want, s)
		}
	}
	if !(strings.Index(s, `"model"`) < strings.Index(s, `"permissions"`) &&
		strings.Index(s, `"hooks"`) < strings.Index(s, `"statusLine"`)) {
		t.Errorf("key order changed:\n%s", s)
	}
	if !strings.Contains(s, "gofmt -w") {
		t.Error("lost the user's own hook")
	}
	var parsed struct {
		Hooks map[string][]struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Command string `json:"command"`
				Timeout int    `json:"timeout"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatal(err)
	}
	for _, ev := range claudeHookEvents {
		groups := parsed.Hooks[ev.name]
		last := groups[len(groups)-1]
		if last.Hooks[0].Command != cmd || last.Hooks[0].Timeout != 5 {
			t.Errorf("%s: %+v", ev.name, last)
		}
		if ev.matcher != (last.Matcher == "*") {
			t.Errorf("%s: matcher %q", ev.name, last.Matcher)
		}
	}
	if len(parsed.Hooks["PostToolUse"]) != 2 {
		t.Errorf("PostToolUse groups: %d", len(parsed.Hooks["PostToolUse"]))
	}

	// Running it again changes nothing.
	if _, changed, _ := editClaudeSettings(out, cmd, false); changed {
		t.Error("second run changed the file")
	}
	// A new germ path replaces the old hooks rather than adding more.
	moved, changed, _ := editClaudeSettings(out, "/opt/germ ctl agent-event claude", false)
	if !changed || strings.Contains(string(moved), "/usr/bin/germ") || strings.Count(string(moved), "agent-event") != len(claudeHookEvents) {
		t.Errorf("path update:\n%s", moved)
	}

	// Removing leaves the user's settings as they were.
	back, changed, _ := editClaudeSettings(out, cmd, true)
	if !changed || strings.Contains(string(back), "agent-event") {
		t.Fatalf("remove:\n%s", back)
	}
	var a, b any
	json.Unmarshal([]byte(userSettings), &a)
	json.Unmarshal(back, &b)
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	if string(ja) != string(jb) {
		t.Errorf("remove didn't restore the settings:\n%s\nvs\n%s", ja, jb)
	}

	// An empty file works, and removing from it drops "hooks" entirely.
	fresh, _, err := editClaudeSettings(nil, cmd, false)
	if err != nil {
		t.Fatal(err)
	}
	empty, _, _ := editClaudeSettings(fresh, cmd, true)
	if strings.TrimSpace(string(empty)) != "{}" {
		t.Errorf("remove from fresh: %s", empty)
	}
	if _, _, err := editClaudeSettings([]byte("[1,2]"), cmd, false); err == nil {
		t.Error("accepted a non-object")
	}
}

func TestSetupClaudeWritesBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	os.WriteFile(path, []byte(userSettings), 0o600)
	if _, err := setupClaude(path, "/usr/bin/germ", false, false); err != nil {
		t.Fatal(err)
	}
	bak, _ := os.ReadFile(path + ".germ-bak")
	if string(bak) != userSettings {
		t.Error("no backup of the original")
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode changed to %v", fi.Mode().Perm())
	}
}

func TestEditCodexConfig(t *testing.T) {
	const self = "/usr/bin/germ"
	cfg := "model = \"o4\"\n\n[tui]\nnotifications = true\n"
	out, _, changed := editCodexConfig(cfg, self, false)
	if !changed || !strings.HasPrefix(out, `notify = ["/usr/bin/germ", "ctl", "agent-event", "codex"]`) || !strings.HasSuffix(out, cfg) {
		t.Fatalf("add:\n%s", out)
	}
	if _, _, changed := editCodexConfig(out, self, false); changed {
		t.Error("second run changed it")
	}
	back, _, _ := editCodexConfig(out, self, true)
	if back != cfg {
		t.Errorf("remove:\n%q", back)
	}
	// Someone else's notify program is left alone.
	other := "notify = [\"notify-send\"]\n" + cfg
	if got, msg, changed := editCodexConfig(other, self, false); changed || got != other || !strings.Contains(msg, "another program") {
		t.Errorf("overwrote another notify: %q", msg)
	}
	// notify inside a table isn't the top-level key.
	tabled := "[profiles.x]\nnotify = [\"x\"]\n"
	if out, _, changed := editCodexConfig(tabled, self, false); !changed || !strings.HasPrefix(out, "notify = [\"/usr/bin/germ\"") {
		t.Errorf("table notify:\n%s", out)
	}
}

func TestClaudeEvent(t *testing.T) {
	for _, c := range []struct {
		in        string
		event     string
		msgSubstr string
	}{
		{`{"hook_event_name":"UserPromptSubmit","prompt":"hi"}`, "working", ""},
		{`{"hook_event_name":"PreToolUse","tool_name":"Bash"}`, "working", ""},
		{`{"hook_event_name":"PermissionRequest","tool_name":"Bash"}`, "waiting", "Bash"},
		{`{"hook_event_name":"Notification","notification_type":"permission_prompt","message":"Claude needs your permission to use Bash"}`, "waiting", "permission"},
		{`{"hook_event_name":"Notification","notification_type":"idle_prompt","message":"Claude is waiting for your input"}`, "", ""},
		{`{"hook_event_name":"Notification","message":"Claude is waiting for your input"}`, "", ""},
		{`{"hook_event_name":"Stop","last_assistant_message":"Done: all tests pass.\n\nDetails..."}`, "done", "Done: all tests pass."},
		{`{"hook_event_name":"StopFailure"}`, "waiting", "error"},
		{`{"hook_event_name":"SessionStart","source":"startup"}`, "idle", ""},
		{`{"hook_event_name":"SessionEnd","reason":"prompt_input_exit"}`, "end", ""},
		{`{"hook_event_name":"PreToolUse","agent_id":"sub1","tool_name":"Read"}`, "", ""},
		{`{"hook_event_name":"FileChanged"}`, "", ""},
		{`not json`, "", ""},
	} {
		ev := claudeEvent([]byte(c.in))
		if ev.event != c.event || !strings.Contains(ev.message, c.msgSubstr) {
			t.Errorf("%s: got %+v", c.in, ev)
		}
		if strings.Contains(ev.message, "\n") {
			t.Errorf("%s: multi-line message", c.in)
		}
	}
	if ev := codexEvent(`{"type":"agent-turn-complete","last-assistant-message":"Fixed it"}`); ev.event != "done" || ev.message != "Fixed it" {
		t.Errorf("codex: %+v", ev)
	}
	if ev := codexEvent(`{"type":"other"}`); ev.event != "" {
		t.Errorf("codex other: %+v", ev)
	}
}

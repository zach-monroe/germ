package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// `germ setup agents` makes Claude Code and Codex report their state to
// germ (see agentevent.go): Claude Code hooks in its settings.json, and
// Codex's notify program in its config.toml. Re-running it updates germ's
// entries; -remove takes them out. Nothing else in either file changes.

const agentEventMarker = "ctl agent-event"

// claudeHookEvents are the Claude Code events germ listens to, and whether
// each takes a tool-name matcher.
var claudeHookEvents = []struct {
	name    string
	matcher bool
}{
	{"SessionStart", false}, {"UserPromptSubmit", false}, {"PreToolUse", true},
	{"PostToolUse", true}, {"PostToolUseFailure", true}, {"PermissionRequest", true},
	{"Notification", false}, {"Stop", false}, {"StopFailure", false}, {"SessionEnd", false},
}

func runSetupAgents(args []string) int {
	fs := flag.NewFlagSet("germ setup agents", flag.ContinueOnError)
	remove := fs.Bool("remove", false, "take germ's hooks out again")
	dry := fs.Bool("n", false, "show what would change without writing")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	self := germCommand()
	failed := false
	for _, step := range []func() (string, error){
		func() (string, error) { return setupClaude(claudeSettingsPath(), self, *remove, *dry) },
		func() (string, error) { return setupCodex(codexConfigPath(), self, *remove, *dry) },
	} {
		msg, err := step()
		if err != nil {
			fmt.Fprintln(os.Stderr, "germ setup agents:", err)
			failed = true
		} else if msg != "" {
			fmt.Println(msg)
		}
	}
	if !*remove && !failed && !*dry {
		fmt.Println("\nRestart running agents to pick this up. Their tab badges and notifications are now exact.")
	}
	if failed {
		return 1
	}
	return 0
}

// germCommand is how hooks should run germ: the copy on $PATH (a stable
// path across upgrades, e.g. Homebrew's bin link), else this binary.
func germCommand() string {
	if p, err := exec.LookPath("germ"); err == nil {
		if abs, err := filepath.Abs(p); err == nil {
			return abs
		}
	}
	if p, err := os.Executable(); err == nil {
		return p
	}
	return "germ"
}

func claudeSettingsPath() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return filepath.Join(d, "settings.json")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "settings.json")
}

func codexConfigPath() string {
	if d := os.Getenv("CODEX_HOME"); d != "" {
		return filepath.Join(d, "config.toml")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".codex", "config.toml")
}

// ---- Claude Code ----------------------------------------------------------

func setupClaude(path, self string, remove, dry bool) (string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		if remove {
			return "Claude Code: no settings file, nothing to remove", nil
		}
		if _, err := exec.LookPath("claude"); err != nil && os.Getenv("CLAUDE_CONFIG_DIR") == "" {
			if _, err := os.Stat(filepath.Dir(path)); err != nil {
				return "Claude Code: not installed, skipped", nil
			}
		}
		data = []byte("{}")
	} else if err != nil {
		return "", err
	}
	out, changed, err := editClaudeSettings(data, hookPath(self)+" ctl agent-event claude", remove)
	if err != nil {
		return "", fmt.Errorf("%s: %v", path, err)
	}
	if !changed {
		if remove {
			return "Claude Code: no germ hooks in " + path, nil
		}
		return "Claude Code: hooks already set up in " + path, nil
	}
	if dry {
		return "Claude Code: would write " + path + ":\n" + string(out), nil
	}
	if err := writeWithBackup(path, out); err != nil {
		return "", err
	}
	if remove {
		return "Claude Code: removed germ's hooks from " + path, nil
	}
	return "Claude Code: added hooks to " + path + " (backup: " + path + ".germ-bak)", nil
}

type jsonField struct {
	key string
	val json.RawMessage
}

// parseObject reads a JSON object keeping its keys in order.
func parseObject(data []byte) ([]jsonField, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, errors.New("not a JSON object")
	}
	var out []jsonField
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		var val json.RawMessage
		if err := dec.Decode(&val); err != nil {
			return nil, err
		}
		out = append(out, jsonField{tok.(string), val})
	}
	return out, nil
}

func writeObject(fields []jsonField) []byte {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, f := range fields {
		if i > 0 {
			b.WriteByte(',')
		}
		k, _ := json.Marshal(f.key)
		b.Write(k)
		b.WriteByte(':')
		b.Write(f.val)
	}
	b.WriteByte('}')
	return b.Bytes()
}

func setField(fields []jsonField, key string, val json.RawMessage) []jsonField {
	for i := range fields {
		if fields[i].key == key {
			fields[i].val = val
			return fields
		}
	}
	return append(fields, jsonField{key, val})
}

func deleteField(fields []jsonField, key string) []jsonField {
	for i := range fields {
		if fields[i].key == key {
			return append(fields[:i:i], fields[i+1:]...)
		}
	}
	return fields
}

// editClaudeSettings adds (or removes) germ's hooks, leaving everything
// else as it was. It reports whether anything changed.
func editClaudeSettings(data []byte, command string, remove bool) ([]byte, bool, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		data = []byte("{}")
	}
	top, err := parseObject(data)
	if err != nil {
		return nil, false, err
	}
	var hooks []jsonField
	for _, f := range top {
		if f.key == "hooks" {
			if hooks, err = parseObject(f.val); err != nil {
				return nil, false, fmt.Errorf("hooks: %v", err)
			}
		}
	}
	before := compact(writeObject(hooks))
	// Take out germ's handlers everywhere (an older germ path, or events
	// germ no longer uses).
	for i := 0; i < len(hooks); i++ {
		groups, kept, err := withoutGerm(hooks[i].val)
		if err != nil {
			return nil, false, fmt.Errorf("hooks.%s: %v", hooks[i].key, err)
		}
		if !kept {
			hooks = append(hooks[:i:i], hooks[i+1:]...)
			i--
			continue
		}
		hooks[i].val = groups
	}
	if !remove {
		for _, ev := range claudeHookEvents {
			group := map[string]any{"hooks": []map[string]any{
				{"type": "command", "command": command, "timeout": 5},
			}}
			if ev.matcher {
				group = map[string]any{"matcher": "*", "hooks": group["hooks"]}
			}
			g, _ := json.Marshal(group)
			var cur []json.RawMessage
			for _, f := range hooks {
				if f.key == ev.name {
					json.Unmarshal(f.val, &cur)
				}
			}
			arr, _ := json.Marshal(append(cur, g))
			hooks = setField(hooks, ev.name, arr)
		}
	}
	if compact(writeObject(hooks)) == before {
		return data, false, nil
	}
	if len(hooks) == 0 {
		top = deleteField(top, "hooks")
	} else {
		top = setField(top, "hooks", writeObject(hooks))
	}
	var out bytes.Buffer
	if err := json.Indent(&out, writeObject(top), "", "  "); err != nil {
		return nil, false, err
	}
	out.WriteByte('\n')
	return out.Bytes(), true, nil
}

func compact(b []byte) string {
	var out bytes.Buffer
	if json.Compact(&out, b) != nil {
		return string(b)
	}
	return out.String()
}

// withoutGerm removes germ's handlers from an event's matcher groups,
// dropping groups left empty. kept is false when nothing remains.
func withoutGerm(raw json.RawMessage) (out json.RawMessage, kept bool, err error) {
	var groups []json.RawMessage
	if err := json.Unmarshal(raw, &groups); err != nil {
		return nil, false, err
	}
	var res []json.RawMessage
	changed := false
	for _, g := range groups {
		fields, err := parseObject(g)
		if err != nil {
			res = append(res, g)
			continue
		}
		var handlers []json.RawMessage
		for _, f := range fields {
			if f.key == "hooks" {
				json.Unmarshal(f.val, &handlers)
			}
		}
		var left []json.RawMessage
		for _, h := range handlers {
			var hh struct {
				Command string `json:"command"`
			}
			if json.Unmarshal(h, &hh) == nil && strings.Contains(hh.Command, agentEventMarker) {
				changed = true
				continue
			}
			left = append(left, h)
		}
		if len(left) == len(handlers) {
			res = append(res, g)
			continue
		}
		if len(left) == 0 {
			continue
		}
		arr, _ := json.Marshal(left)
		res = append(res, writeObject(setField(fields, "hooks", arr)))
	}
	if !changed {
		return raw, len(groups) > 0, nil
	}
	if len(res) == 0 {
		return nil, false, nil
	}
	b, _ := json.Marshal(res)
	return b, true, nil
}

// ---- Codex ----------------------------------------------------------------

var tomlTable = regexp.MustCompile(`^\s*\[`)
var tomlNotify = regexp.MustCompile(`^\s*notify\s*=`)

func setupCodex(path, self string, remove, dry bool) (string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		if remove {
			return "", nil
		}
		if _, err := exec.LookPath("codex"); err != nil {
			return "Codex: not installed, skipped", nil
		}
	} else if err != nil {
		return "", err
	}
	out, msg, changed := editCodexConfig(string(data), self, remove)
	if !changed {
		return msg, nil
	}
	if dry {
		return "Codex: would write " + path + ":\n" + out, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if err := writeWithBackup(path, []byte(out)); err != nil {
		return "", err
	}
	return msg, nil
}

// editCodexConfig sets Codex's top-level notify program to germ, unless
// it's set to something else already (Codex runs only one).
func editCodexConfig(cfg, self string, remove bool) (out, msg string, changed bool) {
	lines := strings.SplitAfter(cfg, "\n")
	notify := -1
	for i, l := range lines {
		if tomlTable.MatchString(l) {
			break // only top-level keys
		}
		if tomlNotify.MatchString(l) {
			notify = i
			break
		}
	}
	want := fmt.Sprintf("notify = [%s, \"ctl\", \"agent-event\", \"codex\"]\n", strconv.Quote(self))
	ours := notify >= 0 && strings.Contains(lines[notify], "agent-event")
	switch {
	case remove && ours:
		lines = append(lines[:notify], lines[notify+1:]...)
		return strings.Join(lines, ""), "Codex: removed germ's notify program", true
	case remove:
		return cfg, "", false
	case ours && lines[notify] == want:
		return cfg, "Codex: notify already set up", false
	case ours:
		lines[notify] = want
		return strings.Join(lines, ""), "Codex: updated germ's notify program", true
	case notify >= 0:
		return cfg, "Codex: notify is already set to another program; to use germ's, set\n  " + strings.TrimSpace(want), false
	}
	return want + cfg, "Codex: set notify to germ (it reports when a turn finishes)", true
}

// ---- files ----------------------------------------------------------------

// writeWithBackup saves the old file as PATH.germ-bak, then replaces it
// atomically, keeping its permissions.
func writeWithBackup(path string, data []byte) error {
	mode := os.FileMode(0o644)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
		old, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.WriteFile(path+".germ-bak", old, mode); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".germ-tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

var plainPath = regexp.MustCompile(`^[A-Za-z0-9_./+-]+$`)

// hookPath quotes a path for a hook command line only when it needs it.
func hookPath(s string) string {
	if plainPath.MatchString(s) {
		return s
	}
	return shellQuote(s)
}

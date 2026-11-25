// Package config loads germ's TOML configuration and watches it for changes.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/zach-monroe/germ/internal/theme"
)

// Config is the user configuration. Zero values mean "use the default".
type Config struct {
	Theme       string            `toml:"theme"`
	Font        Font              `toml:"font"`
	Window      Window            `toml:"window"`
	Scrollback  Scrollback        `toml:"scrollback"`
	Shell       Shell             `toml:"shell"`
	Colors      theme.File        `toml:"colors"`
	Keybindings map[string]string `toml:"keybindings"`
	Tmux        Tmux              `toml:"tmux"`
	Home        Home              `toml:"home"`
	Agents      Agents            `toml:"agents"`
	Control     Control           `toml:"control"`
	Share       Share             `toml:"share"`
	// Widgets configures home-screen widgets by name. Keys other than
	// command and interval are passed to the widget as GERM_WIDGET_<KEY>.
	Widgets map[string]map[string]any `toml:"widgets"`
	Shells  ShellIntegration          `toml:"shell_integration"`
}

type Font struct {
	Family string  `toml:"family"`
	Size   float64 `toml:"size"`
}

type Window struct {
	Padding int `toml:"padding"` // pixels at scale 1
	Columns int `toml:"columns"`
	Rows    int `toml:"rows"`
}

type Scrollback struct {
	Lines int `toml:"lines"`
}

type Shell struct {
	Program string   `toml:"program"`
	Args    []string `toml:"args"`
	// Term sets $TERM. Empty means "xterm-germ" when its terminfo can be
	// installed, else xterm-256color. Set "xterm-256color" if you ssh to
	// hosts without germ's terminfo.
	Term string `toml:"term"`
}

// Tmux configures tmux control-mode integration.
type Tmux struct {
	// Enabled starts every window attached to tmux in control mode
	// (default true; falls back to a plain shell if tmux is missing).
	Enabled bool `toml:"enabled"`
	// Session is the session name to attach to or create. Empty (the
	// default) gives each window its own session: germ, germ-2, ...,
	// reattaching one no other client is using before creating a new one.
	Session string `toml:"session"`
	// Socket selects a tmux server socket name (tmux -L).
	Socket string `toml:"socket"`
}

// Home configures the home tab: a dashboard of widgets shown as the first
// tab.
type Home struct {
	Enabled bool `toml:"enabled"`
	// Layout is rows of widget names, top to bottom. A name may carry a
	// relative width, as in "news:2".
	Layout [][]string `toml:"layout"`
	// RowHeights are relative row heights (default: equal).
	RowHeights []float64 `toml:"row_heights"`
	// Focus shows the home tab when germ starts.
	Focus bool `toml:"focus_on_start"`
}

// Agents configures coding-agent awareness.
type Agents struct {
	// Commands are process names treated as agents.
	Commands []string `toml:"commands"`
	// Notify sends desktop notifications when an agent needs you.
	Notify bool `toml:"notify"`
	// NotifyDone also notifies when an agent that reports its own state
	// (see `germ setup agents`) finishes while you're not looking at it.
	NotifyDone bool `toml:"notify_done"`
	// Launch is the agent `germ agent` (Ctrl+Shift+A) starts; "{task}" is
	// replaced by the task (and dropped when there's none).
	Launch []string `toml:"launch"`
	// Resume restarts the agent in the same worktree, continuing its
	// conversation (default for claude: claude --continue).
	Resume []string `toml:"resume"`
	// WorkingPatterns and WaitingPatterns are matched (case-insensitively)
	// against the bottom of an agent's screen: text an agent shows while
	// working, and text of prompts that wait for you.
	WorkingPatterns []string `toml:"working_patterns"`
	WaitingPatterns []string `toml:"waiting_patterns"`
	// ShiftEnter makes Shift+Enter insert a newline in agent panes (sends
	// ESC CR) when the agent hasn't enabled an extended key protocol.
	ShiftEnter bool `toml:"shift_enter_newline"`
	// Worktrees is where agent worktrees go; "{repo}" is the repository's
	// name. Default: a sibling of the repository, <repo>.worktrees.
	Worktrees string `toml:"worktrees"`
}

// Control configures the control socket used by `germ ctl` and
// `germ mcp`.
type Control struct {
	Enabled bool `toml:"enabled"`
	// AllowInput lets clients type into panes and open new ones (reading
	// is always allowed when enabled).
	AllowInput bool `toml:"allow_input"`
}

// Share configures session sharing (germ share / germ join).
type Share struct {
	// Relay is a relay server to use, e.g. https://relay.example.com (see
	// deploy/relay). Empty (the default): germ share serves the session
	// from this machine, reachable directly or through a free tunnel.
	Relay string `toml:"relay"`
	// Mode, without a relay: "auto" (default: Tailscale Funnel if it's set
	// up, else a Cloudflare quick tunnel), "tailscale", "tunnel", or
	// "direct" (a UPnP port forward and a plain-http link to your public
	// IP; only when chosen explicitly).
	Mode string `toml:"mode"`
	// Approve says whose joins you approve: "all" (default), "typing"
	// (only can-type guests), or "none".
	Approve string `toml:"approve"`
	// Port for direct connections (0: any free port).
	Port int `toml:"port"`
	// PublicAddress is a host name or IP that already forwards Port to
	// this machine (a manual port forward or dynamic DNS); skips UPnP.
	PublicAddress string `toml:"public_address"`
}

// Widget is a resolved widget definition.
type Widget struct {
	Name     string
	Command  []string      // empty: built-in or ~/.config/germ/widgets/NAME
	Interval time.Duration // >0: rerun the command this often and show its output
	Env      []string      // GERM_WIDGET_<KEY>=value options
}

// Widget resolves a widget's configuration.
func (c *Config) Widget(name string) (Widget, error) {
	w := Widget{Name: name}
	keys := make([]string, 0, len(c.Widgets[name]))
	for k := range c.Widgets[name] {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := c.Widgets[name][k]
		switch k {
		case "command":
			switch cmd := v.(type) {
			case string:
				w.Command = []string{"sh", "-c", cmd}
			case []any:
				for _, a := range cmd {
					w.Command = append(w.Command, fmt.Sprint(a))
				}
			default:
				return w, fmt.Errorf("widgets.%s.command: want a string or a list", name)
			}
		case "interval":
			d, err := time.ParseDuration(fmt.Sprint(v))
			if err != nil {
				return w, fmt.Errorf("widgets.%s.interval: %v", name, err)
			}
			w.Interval = d
		default:
			val := fmt.Sprint(v)
			if list, ok := v.([]any); ok { // lists become comma-separated
				parts := make([]string, len(list))
				for i, x := range list {
					parts[i] = fmt.Sprint(x)
				}
				val = strings.Join(parts, ",")
			}
			w.Env = append(w.Env, "GERM_WIDGET_"+strings.ToUpper(k)+"="+val)
		}
	}
	return w, nil
}

// ShellIntegration controls automatic shell integration.
type ShellIntegration struct {
	// Mode is "auto" (default) or "off".
	Mode string `toml:"mode"`
}

// Default returns the built-in configuration.
func Default() Config {
	return Config{
		Theme:      theme.DefaultName,
		Font:       Font{Family: "monospace", Size: 11},
		Window:     Window{Padding: 4, Columns: 100, Rows: 30},
		Scrollback: Scrollback{Lines: 10000},
		Tmux:       Tmux{Enabled: true},
		Home: Home{Enabled: true, Focus: true,
			Layout:     [][]string{{"clock", "weather", "agents"}, {"news:3", "stocks:2"}},
			RowHeights: []float64{2, 3}},
		Control: Control{Enabled: true, AllowInput: true},
		Agents: Agents{Notify: true, NotifyDone: true, Commands: []string{"claude", "codex", "aider", "gemini",
			"opencode", "goose", "amp", "cursor-agent", "crush", "qwen", "copilot"},
			ShiftEnter:      true,
			WorkingPatterns: []string{"esc to interrupt", "ctrl+c to interrupt", "esc to cancel"},
			WaitingPatterns: []string{"do you want to", "would you like to", "❯ 1. yes", "allow this",
				"(y/n)", "[y/n]", "press enter to continue", "waiting for your"},
			Launch: []string{"claude", "{task}"}, Resume: []string{"claude", "--continue"}},
		Shells: ShellIntegration{Mode: "auto"},
	}
}

// Dir returns germ's config directory ($XDG_CONFIG_HOME/germ).
func Dir() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "germ")
}

// Path returns the config file path.
func Path() string { return filepath.Join(Dir(), "config.toml") }

// ThemeDirs returns directories searched for user themes.
func ThemeDirs() []string { return []string{filepath.Join(Dir(), "themes")} }

// Load reads the config file at path, layered over the defaults. A
// missing file is not an error. Warnings (e.g. unknown keys) are returned
// alongside a usable config.
func Load(path string) (Config, []string, error) {
	cfg := Default()
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return cfg, nil, nil
	}
	if err != nil {
		return cfg, nil, err
	}
	return Parse(data)
}

// Parse decodes config data over the defaults.
func Parse(data []byte) (Config, []string, error) {
	cfg := Default()
	md, err := toml.Decode(string(data), &cfg)
	if err != nil {
		return Default(), nil, err
	}
	var warnings []string
	for _, k := range md.Undecoded() {
		warnings = append(warnings, fmt.Sprintf("unknown config key %q", k.String()))
	}
	d := Default()
	if cfg.Font.Size <= 0 {
		cfg.Font.Size = d.Font.Size
	}
	if cfg.Font.Family == "" {
		cfg.Font.Family = d.Font.Family
	}
	if cfg.Window.Columns <= 0 {
		cfg.Window.Columns = d.Window.Columns
	}
	if cfg.Window.Rows <= 0 {
		cfg.Window.Rows = d.Window.Rows
	}
	if cfg.Window.Padding < 0 {
		cfg.Window.Padding = 0
	}
	if cfg.Scrollback.Lines < 0 {
		cfg.Scrollback.Lines = 0
	}
	if cfg.Theme == "" {
		cfg.Theme = d.Theme
	}
	if cfg.Share.Approve == "" {
		cfg.Share.Approve = "all"
	}
	if cfg.Shells.Mode == "" {
		cfg.Shells.Mode = d.Shells.Mode
	}
	if _, err := cfg.Bindings(); err != nil {
		return Default(), warnings, err
	}
	return cfg, warnings, nil
}

// ResolveTheme loads the configured theme and applies [colors] overrides.
func (c *Config) ResolveTheme() (*theme.Theme, error) {
	t, err := theme.Load(c.Theme, ThemeDirs()...)
	if err != nil {
		return nil, err
	}
	if err := c.Colors.Apply(t, false); err != nil {
		return nil, fmt.Errorf("[colors]: %w", err)
	}
	return t, nil
}

// ---- keybindings ----------------------------------------------------------

// Action is something a key binding can do.
type Action string

const (
	ActionNone           Action = "none"
	ActionCopy           Action = "copy"
	ActionPaste          Action = "paste"
	ActionPastePrimary   Action = "paste_primary"
	ActionZoomIn         Action = "zoom_in"
	ActionZoomOut        Action = "zoom_out"
	ActionZoomReset      Action = "zoom_reset"
	ActionScrollPageUp   Action = "scroll_page_up"
	ActionScrollPageDown Action = "scroll_page_down"
	ActionScrollTop      Action = "scroll_top"
	ActionScrollBottom   Action = "scroll_bottom"
	ActionPrevPrompt     Action = "previous_prompt"
	ActionNextPrompt     Action = "next_prompt"
	ActionCopyLastOutput Action = "copy_last_output"
	ActionNewTab         Action = "new_tab"
	ActionCloseTab       Action = "close_tab"
	ActionNextTab        Action = "next_tab"
	ActionPrevTab        Action = "previous_tab"
	ActionSplitRight     Action = "split_right"
	ActionSplitDown      Action = "split_down"
	ActionClosePane      Action = "close_pane"
	ActionFocusLeft      Action = "focus_left"
	ActionFocusRight     Action = "focus_right"
	ActionFocusUp        Action = "focus_up"
	ActionFocusDown      Action = "focus_down"
	ActionZoomPane       Action = "toggle_pane_zoom"
	ActionDetach         Action = "detach"
	ActionReloadConfig   Action = "reload_config"
	ActionHome           Action = "toggle_home"
	ActionNewAgent       Action = "new_agent"
	ActionSendToAgent    Action = "send_to_agent"
	ActionShare          Action = "share_session"
	ActionFollow         Action = "toggle_follow"
	ActionSearch         Action = "search"
	ActionHints          Action = "hints"
	ActionReview         Action = "review_agents"
)

var actions = map[Action]bool{}

func init() {
	for _, a := range []Action{ActionNone, ActionCopy, ActionPaste, ActionPastePrimary,
		ActionZoomIn, ActionZoomOut, ActionZoomReset, ActionScrollPageUp, ActionScrollPageDown,
		ActionScrollTop, ActionScrollBottom, ActionPrevPrompt, ActionNextPrompt, ActionCopyLastOutput,
		ActionNewTab, ActionCloseTab, ActionNextTab, ActionPrevTab, ActionSplitRight, ActionSplitDown,
		ActionClosePane, ActionFocusLeft, ActionFocusRight, ActionFocusUp, ActionFocusDown,
		ActionZoomPane, ActionDetach, ActionReloadConfig, ActionHome, ActionNewAgent, ActionSendToAgent, ActionShare, ActionFollow, ActionSearch, ActionHints, ActionReview} {
		actions[a] = true
	}
}

// Mod bits for key chords.
const (
	ModShift = 1 << iota
	ModAlt
	ModCtrl
	ModSuper
)

// Chord is a key plus modifiers, e.g. ctrl+shift+c. Key is a lowercase
// name: a single character, or one of the names in keyNames.
type Chord struct {
	Mods uint8
	Key  string
}

func (c Chord) String() string {
	var parts []string
	for _, m := range []struct {
		bit  uint8
		name string
	}{{ModCtrl, "ctrl"}, {ModAlt, "alt"}, {ModShift, "shift"}, {ModSuper, "super"}} {
		if c.Mods&m.bit != 0 {
			parts = append(parts, m.name)
		}
	}
	return strings.Join(append(parts, c.Key), "+")
}

var keyNames = map[string]string{
	"pageup": "pageup", "pgup": "pageup", "page_up": "pageup",
	"pagedown": "pagedown", "pgdn": "pagedown", "page_down": "pagedown",
	"home": "home", "end": "end", "insert": "insert", "delete": "delete",
	"up": "up", "down": "down", "left": "left", "right": "right",
	"enter": "enter", "return": "enter", "tab": "tab", "backspace": "backspace",
	"escape": "escape", "esc": "escape", "space": "space",
	"plus": "=", "equal": "=", "minus": "-", "comma": ",", "period": ".",
	"slash": "/", "backslash": "\\", "semicolon": ";", "apostrophe": "'",
	"grave": "`", "leftbracket": "[", "rightbracket": "]",
	"kp_add": "kp_add", "kp_subtract": "kp_subtract",
}

func init() {
	for i := 1; i <= 12; i++ {
		n := fmt.Sprintf("f%d", i)
		keyNames[n] = n
	}
}

// ParseChord parses "ctrl+shift+c".
func ParseChord(s string) (Chord, error) {
	spec := strings.ToLower(strings.TrimSpace(s))
	if strings.HasSuffix(spec, "++") { // "ctrl++" means the plus key
		spec = spec[:len(spec)-1] + "plus"
	}
	parts := strings.Split(spec, "+")
	var c Chord
	for i, p := range parts {
		last := i == len(parts)-1
		switch {
		case !last && (p == "ctrl" || p == "control"):
			c.Mods |= ModCtrl
		case !last && p == "shift":
			c.Mods |= ModShift
		case !last && (p == "alt" || p == "meta"):
			c.Mods |= ModAlt
		case !last && (p == "super" || p == "cmd" || p == "win"):
			c.Mods |= ModSuper
		case last && len([]rune(p)) == 1:
			c.Key = p
		case last && keyNames[p] != "":
			c.Key = keyNames[p]
		default:
			return Chord{}, fmt.Errorf("bad key chord %q", s)
		}
	}
	if c.Key == "" {
		return Chord{}, fmt.Errorf("bad key chord %q", s)
	}
	return c, nil
}

// DefaultBindings are germ's built-in shortcuts. They all use Ctrl+Shift
// (or Shift with navigation keys) to stay out of applications' way.
func DefaultBindings() map[string]Action {
	return map[string]Action{
		"ctrl+shift+c":           ActionCopy,
		"ctrl+shift+v":           ActionPaste,
		"shift+insert":           ActionPaste,
		"ctrl+shift+equal":       ActionZoomIn,
		"ctrl+shift+kp_add":      ActionZoomIn,
		"ctrl+shift+minus":       ActionZoomOut,
		"ctrl+shift+kp_subtract": ActionZoomOut,
		"ctrl+shift+0":           ActionZoomReset,
		"ctrl+shift+backspace":   ActionZoomReset,
		"shift+pageup":           ActionScrollPageUp,
		"shift+pagedown":         ActionScrollPageDown,
		"ctrl+shift+pageup":      ActionScrollPageUp,
		"ctrl+shift+pagedown":    ActionScrollPageDown,
		"shift+home":             ActionScrollTop,
		"shift+end":              ActionScrollBottom,
		"ctrl+shift+up":          ActionPrevPrompt,
		"ctrl+shift+down":        ActionNextPrompt,
		"ctrl+shift+x":           ActionCopyLastOutput,
		"ctrl+shift+t":           ActionNewTab,
		"ctrl+shift+w":           ActionClosePane,
		"ctrl+shift+q":           ActionCloseTab,
		"ctrl+tab":               ActionNextTab,
		"ctrl+shift+tab":         ActionPrevTab,
		"ctrl+shift+right":       ActionNextTab,
		"ctrl+shift+left":        ActionPrevTab,
		"ctrl+shift+enter":       ActionSplitRight,
		"ctrl+shift+d":           ActionSplitDown,
		"ctrl+shift+h":           ActionFocusLeft,
		"ctrl+shift+l":           ActionFocusRight,
		"ctrl+shift+k":           ActionFocusUp,
		"ctrl+shift+j":           ActionFocusDown,
		"ctrl+shift+z":           ActionZoomPane,
		"ctrl+shift+f5":          ActionReloadConfig,
		"ctrl+shift+grave":       ActionHome,
		"ctrl+shift+a":           ActionNewAgent,
		"ctrl+shift+s":           ActionSendToAgent,
		"ctrl+shift+p":           ActionShare,
		"ctrl+shift+g":           ActionFollow,
		"ctrl+shift+f":           ActionSearch,
		"ctrl+shift+e":           ActionHints,
		"ctrl+shift+r":           ActionReview,
	}
}

// Bindings returns the effective chord → action table: defaults, then the
// user's [keybindings] (where "none" removes a binding).
func (c *Config) Bindings() (map[Chord]Action, error) {
	out := map[Chord]Action{}
	add := func(spec string, a Action) error {
		ch, err := ParseChord(spec)
		if err != nil {
			return err
		}
		if !actions[a] {
			return fmt.Errorf("keybinding %q: unknown action %q", spec, a)
		}
		if a == ActionNone {
			delete(out, ch)
		} else {
			out[ch] = a
		}
		return nil
	}
	for spec, a := range DefaultBindings() {
		if err := add(spec, a); err != nil {
			panic(err) // defaults must parse
		}
	}
	keys := make([]string, 0, len(c.Keybindings))
	for k := range c.Keybindings {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, spec := range keys {
		if err := add(spec, Action(c.Keybindings[spec])); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Actions lists all valid action names (for docs and errors).
func Actions() []string {
	var out []string
	for a := range actions {
		out = append(out, string(a))
	}
	sort.Strings(out)
	return out
}

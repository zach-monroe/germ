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
	Shells      ShellIntegration  `toml:"shell_integration"`
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
	// Enabled starts every window attached to tmux in control mode.
	Enabled bool `toml:"enabled"`
	// Session is the session name to attach to or create.
	Session string `toml:"session"`
	// Socket selects a tmux server socket name (tmux -L).
	Socket string `toml:"socket"`
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
		Tmux:       Tmux{Session: "main"},
		Shells:     ShellIntegration{Mode: "auto"},
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
	if cfg.Tmux.Session == "" {
		cfg.Tmux.Session = d.Tmux.Session
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
)

var actions = map[Action]bool{}

func init() {
	for _, a := range []Action{ActionNone, ActionCopy, ActionPaste, ActionPastePrimary,
		ActionZoomIn, ActionZoomOut, ActionZoomReset, ActionScrollPageUp, ActionScrollPageDown,
		ActionScrollTop, ActionScrollBottom, ActionPrevPrompt, ActionNextPrompt, ActionCopyLastOutput,
		ActionNewTab, ActionCloseTab, ActionNextTab, ActionPrevTab, ActionSplitRight, ActionSplitDown,
		ActionClosePane, ActionFocusLeft, ActionFocusRight, ActionFocusUp, ActionFocusDown,
		ActionZoomPane, ActionDetach, ActionReloadConfig} {
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

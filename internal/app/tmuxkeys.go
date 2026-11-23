package app

import (
	"strings"
	"unicode"

	"github.com/go-gl/glfw/v3.3/glfw"

	"github.com/zach-monroe/germ/internal/config"
)

// tmux key bindings in control mode. Keystrokes normally go straight to
// the pane with send-keys, which skips tmux's key tables. So germ watches
// for tmux's prefix key itself and sends the prefix plus the following key
// with `send-keys -K`, which tmux looks up in this client's key tables
// exactly as if they were typed into a regular tmux client.

// parseTmuxKey converts a tmux key name such as "C-b", "M-a", "C-Space"
// or "^A" to a chord. It reports false for "None" and unsupported keys.
func parseTmuxKey(s string) (config.Chord, bool) {
	var c config.Chord
	if len(s) == 2 && s[0] == '^' {
		s = "C-" + s[1:]
	}
	for len(s) > 2 && s[1] == '-' {
		switch s[0] {
		case 'C', 'c':
			c.Mods |= config.ModCtrl
		case 'M', 'm':
			c.Mods |= config.ModAlt
		case 'S', 's':
			c.Mods |= config.ModShift
		default:
			return c, false
		}
		s = s[2:]
	}
	if r := []rune(s); len(r) == 1 {
		if unicode.IsUpper(r[0]) && c.Mods&config.ModCtrl == 0 { // C-A is C-a
			c.Mods |= config.ModShift
		}
		c.Key = string(unicode.ToLower(r[0]))
		return c, true
	}
	names := map[string]string{"space": "space", "escape": "escape", "tab": "tab",
		"enter": "enter", "bspace": "backspace", "ppage": "pageup", "npage": "pagedown",
		"home": "home", "end": "end", "up": "up", "down": "down", "left": "left", "right": "right",
		"ic": "insert", "dc": "delete"}
	l := strings.ToLower(s)
	if n, ok := names[l]; ok {
		c.Key = n
		return c, true
	}
	if len(l) >= 2 && l[0] == 'f' && strings.Trim(l[1:], "0123456789") == "" {
		c.Key = l
		return c, true
	}
	return c, false
}

// tmuxKeyNames maps GLFW keys to tmux key names.
var tmuxKeyNames = map[glfw.Key]string{
	glfw.KeyUp: "Up", glfw.KeyDown: "Down", glfw.KeyLeft: "Left", glfw.KeyRight: "Right",
	glfw.KeyHome: "Home", glfw.KeyEnd: "End", glfw.KeyPageUp: "PPage", glfw.KeyPageDown: "NPage",
	glfw.KeyInsert: "IC", glfw.KeyDelete: "DC", glfw.KeyEnter: "Enter", glfw.KeyKPEnter: "Enter",
	glfw.KeyTab: "Tab", glfw.KeyBackspace: "BSpace", glfw.KeyEscape: "Escape", glfw.KeySpace: "Space",
	glfw.KeyF1: "F1", glfw.KeyF2: "F2", glfw.KeyF3: "F3", glfw.KeyF4: "F4", glfw.KeyF5: "F5",
	glfw.KeyF6: "F6", glfw.KeyF7: "F7", glfw.KeyF8: "F8", glfw.KeyF9: "F9", glfw.KeyF10: "F10",
	glfw.KeyF11: "F11", glfw.KeyF12: "F12",
}

func tmuxMods(name string, ctrl, alt, shift bool) string {
	if shift {
		name = "S-" + name
	}
	if alt {
		name = "M-" + name
	}
	if ctrl {
		name = "C-" + name
	}
	return name
}

// tmuxKeyName names a key press for tmux. Plain printable keys return
// ok=false with wait=true: their character comes from the char callback,
// which knows what Shift produced on this layout.
func tmuxKeyName(key glfw.Key, scancode int, mods glfw.ModifierKey) (name string, ok, wait bool) {
	ctrl, alt, shift := mods&glfw.ModControl != 0, mods&glfw.ModAlt != 0, mods&glfw.ModShift != 0
	switch key {
	case glfw.KeyLeftShift, glfw.KeyRightShift, glfw.KeyLeftControl, glfw.KeyRightControl,
		glfw.KeyLeftAlt, glfw.KeyRightAlt, glfw.KeyLeftSuper, glfw.KeyRightSuper,
		glfw.KeyCapsLock, glfw.KeyNumLock, glfw.KeyScrollLock, glfw.KeyMenu, glfw.KeyUnknown:
		return "", false, true // modifiers: keep waiting
	}
	if n, found := tmuxKeyNames[key]; found {
		if key == glfw.KeyTab && shift && !ctrl && !alt {
			return "BTab", true, false
		}
		return tmuxMods(n, ctrl, alt, shift), true, false
	}
	if !ctrl && !alt {
		return "", false, true
	}
	r, found := baseRune(key, scancode)
	if !found {
		return "", false, false
	}
	return tmuxMods(string(r), ctrl, alt, shift), true, false
}

// tmuxPrefixKey handles a key press in tmux mode. It reports whether the
// key was consumed as (part of) a tmux key binding.
func (a *App) tmuxPrefixKey(key glfw.Key, scancode int, mods glfw.ModifierKey) bool {
	c := a.tmux
	if c == nil || c.client == "" {
		return false
	}
	if c.prefixPending != "" {
		name, ok, wait := tmuxKeyName(key, scancode, mods)
		switch {
		case ok:
			c.sendBinding(name)
		case wait:
			// Modifier, or a printable key whose text arrives in onChar.
		default:
			c.setPrefix("") // a key tmux can't name: give up
		}
		return true
	}
	ch, ok := chordFor(key, scancode, mods)
	if !ok {
		return false
	}
	for _, p := range c.prefixes {
		if p.chord == ch {
			c.setPrefix(p.name)
			return true
		}
	}
	return false
}

// tmuxPrefixChar completes a pending prefix with a typed character.
func (a *App) tmuxPrefixChar(r rune) bool {
	c := a.tmux
	if c == nil || c.prefixPending == "" {
		return false
	}
	c.sendBinding(string(r))
	return true
}

type tmuxPrefix struct {
	name  string
	chord config.Chord
}

func (c *tmuxController) setPrefix(name string) {
	c.prefixPending = name
	c.a.forceRedraw = true // the tab bar shows the prefix state
}

// sendBinding runs the pending prefix followed by key through tmux's key
// tables.
func (c *tmuxController) sendBinding(key string) {
	prefix := c.prefixPending
	c.setPrefix("")
	c.command("send-keys -K -c "+c.client+" "+tmuxQuote(prefix)+" "+tmuxQuote(key), nil)
}

// queryKeys learns this client's name and the prefix keys.
func (c *tmuxController) queryKeys() {
	c.command(`display -p "#{client_name}	#{prefix}	#{prefix2}"`, func(lines []string, failed bool) {
		if failed || len(lines) == 0 {
			return
		}
		f := strings.Split(lines[0], "\t")
		c.client = f[0]
		c.prefixes = nil
		for _, name := range f[1:] {
			if ch, ok := parseTmuxKey(name); ok {
				c.prefixes = append(c.prefixes, tmuxPrefix{name, ch})
			}
		}
	})
}

// paneModeChanged leaves tmux modes (copy mode, choose-tree, ...) as soon
// as a pane enters one: control mode never shows them, so the pane would
// look frozen. germ's own scrollback and selection replace copy mode.
func (c *tmuxController) paneModeChanged(pane string) {
	c.command(`display -p -t `+pane+` "#{pane_in_mode}"`, func(lines []string, failed bool) {
		if failed || len(lines) == 0 || lines[0] == "0" {
			return
		}
		c.command("send-keys -X -t "+pane+" cancel", nil)
	})
}

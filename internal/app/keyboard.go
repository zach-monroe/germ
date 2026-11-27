package app

import (
	"unicode"
	"unicode/utf8"

	"github.com/go-gl/glfw/v3.3/glfw"

	"github.com/zach-monroe/germ/internal/config"
	"github.com/zach-monroe/germ/internal/input"
)

var glfwKeys = map[glfw.Key]input.Key{
	glfw.KeyEnter: input.KeyEnter, glfw.KeyTab: input.KeyTab,
	glfw.KeyBackspace: input.KeyBackspace, glfw.KeyEscape: input.KeyEscape,
	glfw.KeyInsert: input.KeyInsert, glfw.KeyDelete: input.KeyDelete,
	glfw.KeyHome: input.KeyHome, glfw.KeyEnd: input.KeyEnd,
	glfw.KeyPageUp: input.KeyPageUp, glfw.KeyPageDown: input.KeyPageDown,
	glfw.KeyUp: input.KeyUp, glfw.KeyDown: input.KeyDown,
	glfw.KeyLeft: input.KeyLeft, glfw.KeyRight: input.KeyRight,
	glfw.KeyF1: input.KeyF1, glfw.KeyF2: input.KeyF2, glfw.KeyF3: input.KeyF3,
	glfw.KeyF4: input.KeyF4, glfw.KeyF5: input.KeyF5, glfw.KeyF6: input.KeyF6,
	glfw.KeyF7: input.KeyF7, glfw.KeyF8: input.KeyF8, glfw.KeyF9: input.KeyF9,
	glfw.KeyF10: input.KeyF10, glfw.KeyF11: input.KeyF11, glfw.KeyF12: input.KeyF12,
	glfw.KeyKP0: input.KeyKP0, glfw.KeyKP1: input.KeyKP1, glfw.KeyKP2: input.KeyKP2,
	glfw.KeyKP3: input.KeyKP3, glfw.KeyKP4: input.KeyKP4, glfw.KeyKP5: input.KeyKP5,
	glfw.KeyKP6: input.KeyKP6, glfw.KeyKP7: input.KeyKP7, glfw.KeyKP8: input.KeyKP8,
	glfw.KeyKP9: input.KeyKP9, glfw.KeyKPDecimal: input.KeyKPDecimal,
	glfw.KeyKPDivide: input.KeyKPDivide, glfw.KeyKPMultiply: input.KeyKPMultiply,
	glfw.KeyKPSubtract: input.KeyKPSubtract, glfw.KeyKPAdd: input.KeyKPAdd,
	glfw.KeyKPEnter: input.KeyKPEnter, glfw.KeyKPEqual: input.KeyKPEqual,
}

func toMods(m glfw.ModifierKey) input.Mods {
	var out input.Mods
	if m&glfw.ModShift != 0 {
		out |= input.ModShift
	}
	if m&glfw.ModAlt != 0 {
		out |= input.ModAlt
	}
	if m&glfw.ModControl != 0 {
		out |= input.ModCtrl
	}
	if m&glfw.ModSuper != 0 {
		out |= input.ModSuper
	}
	return out
}

// baseRune returns the unshifted character a printable key produces,
// using the active keyboard layout when GLFW knows it.
func baseRune(key glfw.Key, scancode int) (rune, bool) {
	if name := glfw.GetKeyName(key, scancode); name != "" {
		r, _ := utf8.DecodeRuneInString(name)
		return unicode.ToLower(r), true
	}
	if key >= glfw.KeySpace && key <= glfw.KeyGraveAccent {
		return unicode.ToLower(rune(key)), true // GLFW printable keys are ASCII
	}
	return 0, false
}

func (a *App) inputState() input.State {
	p := a.active()
	if p == nil {
		return input.State{}
	}
	p.term.Lock()
	defer p.term.Unlock()
	m := p.term.Modes()
	return input.State{AppCursor: m.AppCursor, AppKeypad: m.AppKeypad,
		KittyFlags: p.term.KeyboardFlags(), ModifyOtherKeys: p.term.ModifyOtherKeys()}
}

func (a *App) onKey(_ *glfw.Window, key glfw.Key, scancode int, action glfw.Action, mods glfw.ModifierKey) {
	if action == glfw.Press {
		a.suppressChar = false
	}
	if key == glfw.KeyLeftControl || key == glfw.KeyRightControl {
		a.updateLinkHover() // Ctrl toggles hyperlink hover
	}
	for _, p := range a.popups() {
		if !p.open {
			continue
		}
		if p.stale {
			p.close() // focus moved on
			continue
		}
		p.key(key, mods, action)
		if action != glfw.Release {
			a.suppressChar = mods&(glfw.ModControl|glfw.ModAlt) != 0 // typed text arrives in onChar
		}
		return
	}
	if a.chat.focused && a.chat.visible() {
		if action == glfw.Release {
			return
		}
		ctrlAlt := mods&(glfw.ModControl|glfw.ModAlt) != 0
		if a.chatKey(key, mods) {
			a.suppressChar = ctrlAlt || key == glfw.KeyEnter || key == glfw.KeyKPEnter
			return
		}
		if a.binding(key, scancode, mods) {
			a.suppressChar = true
			return
		}
		a.suppressChar = ctrlAlt // typed text arrives in onChar
		return
	}
	if action != glfw.Release && a.tmuxPrefixKey(key, scancode, mods) {
		a.suppressChar = a.tmux.prefixPending == "" // the char completes a pending prefix
		return
	}
	if action != glfw.Release && a.binding(key, scancode, mods) {
		a.suppressChar = true
		return
	}

	ev := input.KeyEvent{Mods: toMods(mods), Action: input.Press}
	switch action {
	case glfw.Repeat:
		ev.Action = input.Repeat
	case glfw.Release:
		ev.Action = input.Release
	}
	if k, ok := glfwKeys[key]; ok {
		ev.Key = k
	} else if r, ok := baseRune(key, scancode); ok {
		ev.Key, ev.Rune = input.KeyRune, r
	} else {
		return // modifier keys and unknown keys
	}

	st := a.inputState()
	if b := a.agentShiftEnter(a.active(), ev, st); b != nil {
		a.sendUser(b)
		a.suppressChar = true
		return
	}
	if !input.HandledAsKey(ev, st) {
		return // the char callback will deliver the text
	}
	if b := input.EncodeKey(ev, st); b != nil {
		a.sendUser(b)
	}
	if action != glfw.Release {
		a.suppressChar = true
	}
}

// popup is a pop-up that takes the keyboard while it's open.
type popup struct {
	open  bool
	stale bool // its pane went away or isn't the one in use: close it
	close func()
	key   func(glfw.Key, glfw.ModifierKey, glfw.Action)
	char  func(rune)
}

// popups lists the command palette, hints and scrollback search, in the
// order they get keys (the first open one does).
func (a *App) popups() [3]popup {
	var ps [3]popup
	if s := a.palette; s != nil {
		ps[0] = popup{true, a.tabOf(s.pane) != a.tab() || s.pane.exited.Load(), a.closePalette, a.paletteKey, a.paletteType}
	}
	if s := a.hints; s != nil {
		ps[1] = popup{true, s.pane != a.active() || s.pane.exited.Load(), a.closeHints, a.hintsKey, a.hintsType}
	}
	if s := a.search; s != nil {
		ps[2] = popup{true, s.pane != a.active() || s.pane.exited.Load(), a.closeSearch, a.searchKey,
			func(r rune) { a.searchType(string(r)) }}
	}
	return ps
}

// onChar handles printable text (already composed by the OS/IME).
func (a *App) onChar(_ *glfw.Window, r rune, mods glfw.ModifierKey) {
	if a.suppressChar {
		return
	}
	for _, p := range a.popups() {
		if p.open {
			p.char(r)
			return
		}
	}
	if a.chat.focused && a.chat.visible() {
		a.chatType(r)
		return
	}
	if a.tmuxPrefixChar(r) {
		return
	}
	a.sendUser(input.EncodeText(r, toMods(mods)&input.ModAlt, input.State{}))
}

// glfwKeyNames maps non-character keys to config chord names.
var glfwKeyNames = map[glfw.Key]string{
	glfw.KeyPageUp: "pageup", glfw.KeyPageDown: "pagedown", glfw.KeyHome: "home",
	glfw.KeyEnd: "end", glfw.KeyInsert: "insert", glfw.KeyDelete: "delete",
	glfw.KeyUp: "up", glfw.KeyDown: "down", glfw.KeyLeft: "left", glfw.KeyRight: "right",
	glfw.KeyEnter: "enter", glfw.KeyKPEnter: "enter", glfw.KeyTab: "tab",
	glfw.KeyBackspace: "backspace", glfw.KeyEscape: "escape", glfw.KeySpace: "space",
	glfw.KeyKPAdd: "kp_add", glfw.KeyKPSubtract: "kp_subtract",
	glfw.KeyF1: "f1", glfw.KeyF2: "f2", glfw.KeyF3: "f3", glfw.KeyF4: "f4",
	glfw.KeyF5: "f5", glfw.KeyF6: "f6", glfw.KeyF7: "f7", glfw.KeyF8: "f8",
	glfw.KeyF9: "f9", glfw.KeyF10: "f10", glfw.KeyF11: "f11", glfw.KeyF12: "f12",
}

func chordFor(key glfw.Key, scancode int, mods glfw.ModifierKey) (config.Chord, bool) {
	var c config.Chord
	if mods&glfw.ModShift != 0 {
		c.Mods |= config.ModShift
	}
	if mods&glfw.ModAlt != 0 {
		c.Mods |= config.ModAlt
	}
	if mods&glfw.ModControl != 0 {
		c.Mods |= config.ModCtrl
	}
	if mods&glfw.ModSuper != 0 {
		c.Mods |= config.ModSuper
	}
	if name, ok := glfwKeyNames[key]; ok {
		c.Key = name
		return c, true
	}
	// Character keys: use the US-layout key identity so bindings like
	// ctrl+shift+c work regardless of what Shift produces.
	if key >= glfw.KeySpace && key <= glfw.KeyGraveAccent {
		c.Key = string(unicode.ToLower(rune(key)))
		return c, true
	}
	if r, ok := baseRune(key, scancode); ok {
		c.Key = string(r)
		return c, true
	}
	return c, false
}

// binding runs a configured shortcut for this key, reporting whether it
// consumed the key.
func (a *App) binding(key glfw.Key, scancode int, mods glfw.ModifierKey) bool {
	ch, ok := chordFor(key, scancode, mods)
	if !ok {
		return false
	}
	act, ok := a.bindings[ch]
	if !ok {
		return false
	}
	return a.do(act)
}

// do performs an action. It returns false for actions that don't apply
// right now, so the key falls through to the application.
func (a *App) do(act config.Action) bool {
	switch act {
	case config.ActionScrollPageUp, config.ActionScrollPageDown, config.ActionScrollTop, config.ActionScrollBottom:
		p := a.active()
		if p == nil {
			return false
		}
		p.term.Lock()
		alt := p.term.AltScreen()
		p.term.Unlock()
		if alt {
			return false // no scrollback: let full-screen apps have the key
		}
	}
	switch act {
	case config.ActionCopy:
		a.copySelection()
	case config.ActionPaste:
		a.pasteClipboard()
	case config.ActionPastePrimary:
		a.pastePrimary()
	case config.ActionZoomIn:
		a.setFontSize(a.fontSize + 1)
	case config.ActionZoomOut:
		a.setFontSize(a.fontSize - 1)
	case config.ActionZoomReset:
		a.setFontSize(a.cfg.Font.Size)
	case config.ActionScrollPageUp:
		a.scrollBy(a.pageLines())
	case config.ActionScrollPageDown:
		a.scrollBy(-a.pageLines())
	case config.ActionScrollTop:
		a.scrollBy(1 << 30)
	case config.ActionScrollBottom:
		a.scrollBy(-(1 << 30))
	case config.ActionReloadConfig:
		a.reloadConfig()
	default:
		return a.doExtended(act)
	}
	return true
}

func (a *App) pageLines() int {
	p := a.active()
	if p == nil {
		return 1
	}
	p.term.Lock()
	defer p.term.Unlock()
	_, rows := p.term.Size()
	return max(1, rows-1)
}

// scrollBy moves the viewport n lines back into history (negative: forward).
func (a *App) scrollBy(n int) {
	p := a.active()
	if p == nil {
		return
	}
	p.term.Lock()
	sb := p.term.ScrollbackLen()
	p.term.Unlock()
	off := max(0, min(p.offset+n, sb))
	if off != p.offset {
		p.offset = off
		a.forceRedraw = true
	}
}

func inputFocus(focused bool) []byte { return input.EncodeFocus(focused) }

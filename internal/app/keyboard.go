package app

import (
	"unicode"
	"unicode/utf8"

	"github.com/go-gl/glfw/v3.3/glfw"

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
	a.term.Lock()
	defer a.term.Unlock()
	m := a.term.Modes()
	return input.State{AppCursor: m.AppCursor, AppKeypad: m.AppKeypad, KittyFlags: a.term.KeyboardFlags()}
}

func (a *App) onKey(_ *glfw.Window, key glfw.Key, scancode int, action glfw.Action, mods glfw.ModifierKey) {
	if action == glfw.Press {
		a.suppressChar = false
	}
	if key == glfw.KeyLeftControl || key == glfw.KeyRightControl {
		a.updateLinkHover() // Ctrl toggles hyperlink hover
	}
	if action != glfw.Release && a.binding(key, mods) {
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

// onChar handles printable text (already composed by the OS/IME).
func (a *App) onChar(_ *glfw.Window, r rune, mods glfw.ModifierKey) {
	if a.suppressChar {
		return
	}
	a.sendUser(input.EncodeText(r, toMods(mods)&input.ModAlt, input.State{}))
}

// binding handles germ's own shortcuts. All use Ctrl+Shift (or Shift with
// navigation keys) so they don't collide with application key bindings.
// Phase 4 makes these configurable.
func (a *App) binding(key glfw.Key, mods glfw.ModifierKey) bool {
	cs := glfw.ModControl | glfw.ModShift
	switch {
	case mods&^glfw.ModNumLock&^glfw.ModCapsLock == cs:
		switch key {
		case glfw.KeyC:
			a.copySelection()
		case glfw.KeyV:
			a.pasteClipboard()
		case glfw.KeyEqual, glfw.KeyKPAdd:
			a.setFontSize(a.fontSize + 1)
		case glfw.KeyMinus, glfw.KeyKPSubtract:
			a.setFontSize(a.fontSize - 1)
		case glfw.Key0, glfw.KeyBackspace:
			a.setFontSize(a.opts.FontSize)
		case glfw.KeyPageUp:
			a.scrollBy(a.pageLines())
		case glfw.KeyPageDown:
			a.scrollBy(-a.pageLines())
		default:
			return false
		}
		return true
	case mods&^glfw.ModNumLock&^glfw.ModCapsLock == glfw.ModShift:
		switch key {
		case glfw.KeyPageUp:
			a.scrollBy(a.pageLines())
		case glfw.KeyPageDown:
			a.scrollBy(-a.pageLines())
		case glfw.KeyInsert:
			a.pasteClipboard()
		default:
			return false
		}
		return true
	}
	return false
}

func (a *App) pageLines() int {
	a.term.Lock()
	defer a.term.Unlock()
	_, rows := a.term.Size()
	return max(1, rows-1)
}

// scrollBy moves the viewport n lines back into history (negative: forward).
func (a *App) scrollBy(n int) {
	a.term.Lock()
	sb := a.term.ScrollbackLen()
	a.term.Unlock()
	off := max(0, min(a.offset+n, sb))
	if off != a.offset {
		a.offset = off
		a.forceRedraw = true
	}
}

func inputFocus(focused bool) []byte { return input.EncodeFocus(focused) }

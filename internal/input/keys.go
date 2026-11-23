// Package input encodes keyboard, mouse, and paste events into the byte
// sequences a terminal application expects. It has no windowing
// dependency; the app translates GLFW events into these types.
package input

import (
	"strconv"
	"unicode"
	"unicode/utf8"

	"github.com/zach-monroe/germ/internal/term"
)

// Mods is a set of modifier keys. The values match the xterm and kitty
// modifier encodings (parameter = 1 + mods).
type Mods uint8

const (
	ModShift Mods = 1 << iota
	ModAlt
	ModCtrl
	ModSuper
)

// Action is a key or button transition.
type Action uint8

const (
	Press Action = iota + 1
	Repeat
	Release
)

// Key identifies a non-text key. KeyRune means a key that produces text;
// its base (unshifted) character is in KeyEvent.Rune.
type Key uint16

const (
	KeyRune Key = iota
	KeyEnter
	KeyTab
	KeyBackspace
	KeyEscape
	KeyInsert
	KeyDelete
	KeyHome
	KeyEnd
	KeyPageUp
	KeyPageDown
	KeyUp
	KeyDown
	KeyLeft
	KeyRight
	KeyF1
	KeyF2
	KeyF3
	KeyF4
	KeyF5
	KeyF6
	KeyF7
	KeyF8
	KeyF9
	KeyF10
	KeyF11
	KeyF12
	KeyKP0
	KeyKP1
	KeyKP2
	KeyKP3
	KeyKP4
	KeyKP5
	KeyKP6
	KeyKP7
	KeyKP8
	KeyKP9
	KeyKPDecimal
	KeyKPDivide
	KeyKPMultiply
	KeyKPSubtract
	KeyKPAdd
	KeyKPEnter
	KeyKPEqual
)

// KeyEvent is a key transition.
type KeyEvent struct {
	Key    Key
	Rune   rune // base character for KeyRune (e.g. 'a' even with Shift)
	Mods   Mods
	Action Action
}

// State is the terminal state that affects encoding.
type State struct {
	AppCursor  bool
	AppKeypad  bool
	KittyFlags uint8
}

// IsKeypad reports whether k is on the numeric keypad.
func (k Key) IsKeypad() bool { return k >= KeyKP0 && k <= KeyKPEqual }

// HandledAsKey reports whether a key event should be encoded from the key
// callback (true) or left to the text/char callback (false). Text input
// goes through the char callback so keyboard layouts and IMEs work.
func HandledAsKey(ev KeyEvent, st State) bool {
	all := st.KittyFlags&term.KittyAllKeys != 0
	switch {
	case ev.Key == KeyRune:
		if all {
			return true
		}
		if ev.Action == Release {
			return false
		}
		// Ctrl combos never produce char events on X11. With kitty
		// disambiguation, Alt combos are encoded as keys too.
		return ev.Mods&ModCtrl != 0 ||
			(st.KittyFlags&term.KittyDisambiguate != 0 && ev.Mods&(ModAlt|ModSuper) != 0)
	case ev.Key.IsKeypad() && ev.Key != KeyKPEnter:
		// Numeric keypad digits produce chars unless in application mode.
		return all || st.AppKeypad || ev.Mods&(ModCtrl|ModAlt) != 0
	}
	return true
}

// EncodeKey returns the bytes for a key event, or nil if the event should
// not be sent (e.g. a release when release reporting is off).
func EncodeKey(ev KeyEvent, st State) []byte {
	if st.KittyFlags != 0 {
		return encodeKitty(ev, st)
	}
	if ev.Action == Release {
		return nil
	}
	return encodeLegacy(ev, st)
}

// EncodeText returns the bytes for text from the char callback.
func EncodeText(r rune, mods Mods, st State) []byte {
	b := utf8.AppendRune(nil, r)
	if mods&ModAlt != 0 {
		return append([]byte{0x1b}, b...) // xterm metaSendsEscape
	}
	return b
}

// csiMod formats the modifier parameter (1 + mods).
func modParam(m Mods) int { return 1 + int(m&(ModShift|ModAlt|ModCtrl|ModSuper)) }

// legacy encodings for cursor/function keys: final byte and whether the
// unmodified form uses SS3.
type special struct {
	final byte
	num   int  // for "CSI num ~" keys
	ss3   bool // unmodified form is SS3 (F1–F4) or SS3 in app cursor mode
}

var specials = map[Key]special{
	KeyUp: {final: 'A', ss3: true}, KeyDown: {final: 'B', ss3: true},
	KeyRight: {final: 'C', ss3: true}, KeyLeft: {final: 'D', ss3: true},
	KeyHome: {final: 'H', ss3: true}, KeyEnd: {final: 'F', ss3: true},
	KeyF1: {final: 'P', ss3: true}, KeyF2: {final: 'Q', ss3: true},
	KeyF3: {final: 'R', ss3: true}, KeyF4: {final: 'S', ss3: true},
	KeyInsert: {final: '~', num: 2}, KeyDelete: {final: '~', num: 3},
	KeyPageUp: {final: '~', num: 5}, KeyPageDown: {final: '~', num: 6},
	KeyF5: {final: '~', num: 15}, KeyF6: {final: '~', num: 17},
	KeyF7: {final: '~', num: 18}, KeyF8: {final: '~', num: 19},
	KeyF9: {final: '~', num: 20}, KeyF10: {final: '~', num: 21},
	KeyF11: {final: '~', num: 23}, KeyF12: {final: '~', num: 24},
}

// keypad application-mode SS3 finals and numeric-mode characters.
var keypad = map[Key]struct {
	ss3  byte
	char byte
}{
	KeyKP0: {'p', '0'}, KeyKP1: {'q', '1'}, KeyKP2: {'r', '2'}, KeyKP3: {'s', '3'},
	KeyKP4: {'t', '4'}, KeyKP5: {'u', '5'}, KeyKP6: {'v', '6'}, KeyKP7: {'w', '7'},
	KeyKP8: {'x', '8'}, KeyKP9: {'y', '9'}, KeyKPDecimal: {'n', '.'},
	KeyKPDivide: {'o', '/'}, KeyKPMultiply: {'j', '*'}, KeyKPSubtract: {'m', '-'},
	KeyKPAdd: {'k', '+'}, KeyKPEnter: {'M', '\r'}, KeyKPEqual: {'X', '='},
}

func encodeLegacy(ev KeyEvent, st State) []byte {
	m := ev.Mods
	alt := func(b []byte) []byte {
		if m&ModAlt != 0 {
			return append([]byte{0x1b}, b...)
		}
		return b
	}
	if sp, ok := specials[ev.Key]; ok {
		return legacySpecial(sp, m, st.AppCursor || ev.Key >= KeyF1 && ev.Key <= KeyF4)
	}
	if kp, ok := keypad[ev.Key]; ok {
		if st.AppKeypad && m == 0 {
			return []byte{0x1b, 'O', kp.ss3}
		}
		return alt([]byte{kp.char})
	}
	switch ev.Key {
	case KeyEnter:
		return alt([]byte{'\r'})
	case KeyTab:
		if m&ModShift != 0 {
			return []byte("\x1b[Z")
		}
		return alt([]byte{'\t'})
	case KeyBackspace:
		if m&ModCtrl != 0 {
			return alt([]byte{0x08})
		}
		return alt([]byte{0x7f})
	case KeyEscape:
		return alt([]byte{0x1b})
	case KeyRune:
		if m&ModCtrl != 0 {
			if c, ok := ctrlByte(ev.Rune); ok {
				return alt([]byte{c})
			}
		}
		r := ev.Rune
		if m&ModShift != 0 {
			r = unicode.ToUpper(r)
		}
		return alt(utf8.AppendRune(nil, r))
	}
	return nil
}

func legacySpecial(sp special, m Mods, ss3 bool) []byte {
	if sp.final == '~' {
		if m == 0 {
			return []byte("\x1b[" + strconv.Itoa(sp.num) + "~")
		}
		return []byte("\x1b[" + strconv.Itoa(sp.num) + ";" + strconv.Itoa(modParam(m)) + "~")
	}
	if m == 0 {
		if sp.ss3 && ss3 {
			return []byte{0x1b, 'O', sp.final}
		}
		return []byte{0x1b, '[', sp.final}
	}
	return []byte("\x1b[1;" + strconv.Itoa(modParam(m)) + string(sp.final))
}

// ctrlByte maps Ctrl+key to its C0 control, following xterm.
func ctrlByte(r rune) (byte, bool) {
	switch {
	case r >= 'a' && r <= 'z':
		return byte(r-'a') + 1, true
	case r >= 'A' && r <= 'Z':
		return byte(r-'A') + 1, true
	}
	switch r {
	case ' ', '@', '2', '`':
		return 0, true
	case '[', '3':
		return 0x1b, true
	case '\\', '4':
		return 0x1c, true
	case ']', '5':
		return 0x1d, true
	case '^', '6', '~':
		return 0x1e, true
	case '_', '7', '/', '-':
		return 0x1f, true
	case '8', '?':
		return 0x7f, true
	}
	return 0, false
}

// kitty functional key codes (CSI code u) for keys without a legacy CSI form.
var kittyCodes = map[Key]int{
	KeyEscape: 27, KeyEnter: 13, KeyTab: 9, KeyBackspace: 127,
	KeyKP0: 57399, KeyKP1: 57400, KeyKP2: 57401, KeyKP3: 57402, KeyKP4: 57403,
	KeyKP5: 57404, KeyKP6: 57405, KeyKP7: 57406, KeyKP8: 57407, KeyKP9: 57408,
	KeyKPDecimal: 57409, KeyKPDivide: 57410, KeyKPMultiply: 57411,
	KeyKPSubtract: 57412, KeyKPAdd: 57413, KeyKPEnter: 57414, KeyKPEqual: 57415,
}

func encodeKitty(ev KeyEvent, st State) []byte {
	flags := st.KittyFlags
	all := flags&term.KittyAllKeys != 0
	events := flags&term.KittyEventTypes != 0
	m := ev.Mods

	if ev.Action == Release && !events {
		return nil
	}
	// Without "report all keys", Enter/Tab/Backspace never report release.
	if ev.Action == Release && !all && (ev.Key == KeyEnter || ev.Key == KeyTab || ev.Key == KeyBackspace) {
		return nil
	}

	// Keys that keep their legacy encoding unless "all keys" is set.
	if !all && ev.Action != Release {
		switch {
		case ev.Key == KeyEnter || ev.Key == KeyTab || ev.Key == KeyBackspace:
			if m == 0 || (ev.Key == KeyTab && m == ModShift) {
				return encodeLegacy(ev, State{})
			}
		case ev.Key == KeyRune && m&^ModShift == 0:
			// Plain or shifted text: handled by the char callback.
			return encodeLegacy(ev, State{})
		case ev.Key.IsKeypad() && m == 0 && !st.AppKeypad:
			return encodeLegacy(ev, st)
		}
	}

	// Event type suffix ":2" (repeat) or ":3" (release).
	modStr := ""
	needMods := m != 0 || (events && ev.Action != Press)
	if needMods {
		modStr = strconv.Itoa(modParam(m))
		if events && ev.Action == Repeat {
			modStr += ":2"
		} else if events && ev.Action == Release {
			modStr += ":3"
		}
	}

	if sp, ok := specials[ev.Key]; ok {
		if sp.final == '~' {
			s := "\x1b[" + strconv.Itoa(sp.num)
			if needMods {
				s += ";" + modStr
			}
			return []byte(s + "~")
		}
		if !needMods {
			if ev.Key >= KeyF1 && ev.Key <= KeyF4 {
				return []byte{0x1b, '[', sp.final}
			}
			return legacySpecial(sp, 0, st.AppCursor)
		}
		return []byte("\x1b[1;" + modStr + string(sp.final))
	}

	code, ok := kittyCodes[ev.Key]
	if ev.Key == KeyRune {
		code, ok = int(unicode.ToLower(ev.Rune)), true
	}
	if !ok {
		return nil
	}
	s := "\x1b[" + strconv.Itoa(code)
	if needMods {
		s += ";" + modStr
	}
	return []byte(s + "u")
}

package input

import (
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/zach-monroe/germ/internal/term"
)

// Button is a mouse button as encoded in reports.
type Button uint8

const (
	ButtonLeft      Button = 0
	ButtonMiddle    Button = 1
	ButtonRight     Button = 2
	ButtonNone      Button = 3 // motion with no button held
	ButtonWheelUp   Button = 64
	ButtonWheelDown Button = 65
	ButtonWheelLeft Button = 66
	ButtonWheelRite Button = 67
)

// MouseAction distinguishes press, release, and motion.
type MouseAction uint8

const (
	MousePress MouseAction = iota
	MouseRelease
	MouseMotion
)

// MouseEvent is a mouse event in cell and pixel coordinates (0-based).
type MouseEvent struct {
	Button Button
	Action MouseAction
	X, Y   int // cell
	PX, PY int // pixel, for SGR-pixels mode
	Mods   Mods
}

func (b Button) isWheel() bool { return b >= 64 }

// EncodeMouse returns the report for ev under the given mode and
// encoding, or nil if the mode doesn't report this event.
func EncodeMouse(ev MouseEvent, mode term.MouseMode, enc term.MouseEncoding) []byte {
	switch mode {
	case term.MouseNone:
		return nil
	case term.MouseX10:
		if ev.Action != MousePress {
			return nil
		}
		ev.Mods = 0
	case term.MouseNormal:
		if ev.Action == MouseMotion {
			return nil
		}
	case term.MouseButton:
		if ev.Action == MouseMotion && ev.Button == ButtonNone {
			return nil
		}
	}
	if ev.Action == MouseRelease && ev.Button.isWheel() {
		return nil // wheels have no release
	}

	cb := int(ev.Button)
	if ev.Mods&ModShift != 0 {
		cb += 4
	}
	if ev.Mods&ModAlt != 0 {
		cb += 8
	}
	if ev.Mods&ModCtrl != 0 {
		cb += 16
	}
	if ev.Action == MouseMotion {
		cb += 32
	}

	switch enc {
	case term.MouseEncSGR, term.MouseEncSGRPixels:
		x, y := ev.X+1, ev.Y+1
		if enc == term.MouseEncSGRPixels {
			x, y = ev.PX+1, ev.PY+1
		}
		final := "M"
		if ev.Action == MouseRelease {
			final = "m"
		}
		return []byte("\x1b[<" + strconv.Itoa(cb) + ";" + strconv.Itoa(x) + ";" + strconv.Itoa(y) + final)
	}

	// Legacy encodings report release as button 3.
	if ev.Action == MouseRelease {
		cb = cb&^3 | 3
	}
	if enc == term.MouseEncURXVT {
		return []byte("\x1b[" + strconv.Itoa(cb+32) + ";" + strconv.Itoa(ev.X+1) + ";" + strconv.Itoa(ev.Y+1) + "M")
	}
	out := []byte("\x1b[M")
	for _, v := range []int{cb + 32, ev.X + 33, ev.Y + 33} {
		if enc == term.MouseEncUTF8 {
			if v > 2047 {
				return nil
			}
			out = utf8.AppendRune(out, rune(v))
			continue
		}
		if v > 255 {
			return nil // coordinate not representable
		}
		out = append(out, byte(v))
	}
	return out
}

// EncodePaste prepares clipboard text for the child: newlines become CR,
// and with bracketed paste the text is wrapped in ESC[200~ … ESC[201~ with
// any embedded end marker removed so pasted text cannot break out.
func EncodePaste(text string, bracketed bool) []byte {
	text = strings.ReplaceAll(text, "\r\n", "\r")
	text = strings.ReplaceAll(text, "\n", "\r")
	if !bracketed {
		return []byte(text)
	}
	for strings.Contains(text, "\x1b[201~") {
		text = strings.ReplaceAll(text, "\x1b[201~", "")
	}
	return []byte("\x1b[200~" + text + "\x1b[201~")
}

// EncodeFocus returns the focus-change report (mode 1004).
func EncodeFocus(focused bool) []byte {
	if focused {
		return []byte("\x1b[I")
	}
	return []byte("\x1b[O")
}

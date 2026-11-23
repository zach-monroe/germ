package input

import (
	"strings"
	"testing"

	"github.com/zach-monroe/germ/internal/term"
)

func TestLegacyKeys(t *testing.T) {
	tests := []struct {
		ev   KeyEvent
		st   State
		want string
	}{
		{KeyEvent{Key: KeyEnter}, State{}, "\r"},
		{KeyEvent{Key: KeyEnter, Mods: ModAlt}, State{}, "\x1b\r"},
		{KeyEvent{Key: KeyTab, Mods: ModShift}, State{}, "\x1b[Z"},
		{KeyEvent{Key: KeyBackspace}, State{}, "\x7f"},
		{KeyEvent{Key: KeyBackspace, Mods: ModCtrl}, State{}, "\x08"},
		{KeyEvent{Key: KeyUp}, State{}, "\x1b[A"},
		{KeyEvent{Key: KeyUp}, State{AppCursor: true}, "\x1bOA"},
		{KeyEvent{Key: KeyUp, Mods: ModCtrl}, State{AppCursor: true}, "\x1b[1;5A"},
		{KeyEvent{Key: KeyLeft, Mods: ModShift | ModAlt}, State{}, "\x1b[1;4D"},
		{KeyEvent{Key: KeyHome}, State{}, "\x1b[H"},
		{KeyEvent{Key: KeyF1}, State{}, "\x1bOP"},
		{KeyEvent{Key: KeyF1, Mods: ModShift}, State{}, "\x1b[1;2P"},
		{KeyEvent{Key: KeyF5}, State{}, "\x1b[15~"},
		{KeyEvent{Key: KeyF12, Mods: ModCtrl}, State{}, "\x1b[24;5~"},
		{KeyEvent{Key: KeyDelete}, State{}, "\x1b[3~"},
		{KeyEvent{Key: KeyPageUp, Mods: ModShift}, State{}, "\x1b[5;2~"},
		{KeyEvent{Key: KeyRune, Rune: 'c', Mods: ModCtrl}, State{}, "\x03"},
		{KeyEvent{Key: KeyRune, Rune: 'c', Mods: ModCtrl | ModAlt}, State{}, "\x1b\x03"},
		{KeyEvent{Key: KeyRune, Rune: ' ', Mods: ModCtrl}, State{}, "\x00"},
		{KeyEvent{Key: KeyRune, Rune: '[', Mods: ModCtrl}, State{}, "\x1b"},
		{KeyEvent{Key: KeyRune, Rune: '/', Mods: ModCtrl}, State{}, "\x1f"},
		{KeyEvent{Key: KeyKP5}, State{AppKeypad: true}, "\x1bOu"},
		{KeyEvent{Key: KeyKPEnter}, State{}, "\r"},
		{KeyEvent{Key: KeyKPEnter}, State{AppKeypad: true}, "\x1bOM"},
		{KeyEvent{Key: KeyUp, Action: Release}, State{}, ""},
	}
	for _, tt := range tests {
		if tt.ev.Action == 0 {
			tt.ev.Action = Press
		}
		if got := string(EncodeKey(tt.ev, tt.st)); got != tt.want {
			t.Errorf("%+v %+v: got %q, want %q", tt.ev, tt.st, got, tt.want)
		}
	}
}

func TestKittyKeys(t *testing.T) {
	d := State{KittyFlags: term.KittyDisambiguate}
	de := State{KittyFlags: term.KittyDisambiguate | term.KittyEventTypes}
	all := State{KittyFlags: term.KittyDisambiguate | term.KittyAllKeys}
	tests := []struct {
		ev   KeyEvent
		st   State
		want string
	}{
		{KeyEvent{Key: KeyEscape}, d, "\x1b[27u"},
		{KeyEvent{Key: KeyEnter}, d, "\r"},
		{KeyEvent{Key: KeyEnter, Mods: ModShift}, d, "\x1b[13;2u"},
		{KeyEvent{Key: KeyBackspace, Mods: ModCtrl}, d, "\x1b[127;5u"},
		{KeyEvent{Key: KeyRune, Rune: 'a', Mods: ModCtrl}, d, "\x1b[97;5u"},
		{KeyEvent{Key: KeyRune, Rune: 'i', Mods: ModCtrl}, d, "\x1b[105;5u"}, // distinct from Tab
		{KeyEvent{Key: KeyRune, Rune: 'a', Mods: ModAlt}, d, "\x1b[97;3u"},
		{KeyEvent{Key: KeyRune, Rune: 'a', Mods: ModCtrl | ModShift}, d, "\x1b[97;6u"},
		{KeyEvent{Key: KeyUp}, d, "\x1b[A"},
		{KeyEvent{Key: KeyUp, Mods: ModCtrl}, d, "\x1b[1;5A"},
		{KeyEvent{Key: KeyF1}, d, "\x1b[P"},
		{KeyEvent{Key: KeyF5, Mods: ModAlt}, d, "\x1b[15;3~"},
		{KeyEvent{Key: KeyRune, Rune: 'a', Mods: ModCtrl, Action: Release}, d, ""},
		{KeyEvent{Key: KeyRune, Rune: 'a', Mods: ModCtrl, Action: Release}, de, "\x1b[97;5:3u"},
		{KeyEvent{Key: KeyRune, Rune: 'a', Mods: ModCtrl, Action: Repeat}, de, "\x1b[97;5:2u"},
		{KeyEvent{Key: KeyUp, Action: Release}, de, "\x1b[1;1:3A"},
		{KeyEvent{Key: KeyEnter, Action: Release}, de, ""},
		{KeyEvent{Key: KeyRune, Rune: 'a'}, all, "\x1b[97u"},
		{KeyEvent{Key: KeyRune, Rune: 'a', Mods: ModShift}, all, "\x1b[97;2u"},
		{KeyEvent{Key: KeyEnter}, all, "\x1b[13u"},
		{KeyEvent{Key: KeyKP1}, all, "\x1b[57400u"},
	}
	for _, tt := range tests {
		if tt.ev.Action == 0 {
			tt.ev.Action = Press
		}
		if got := string(EncodeKey(tt.ev, tt.st)); got != tt.want {
			t.Errorf("%+v flags=%d: got %q, want %q", tt.ev, tt.st.KittyFlags, got, tt.want)
		}
	}
}

func TestHandledAsKey(t *testing.T) {
	d := State{KittyFlags: term.KittyDisambiguate}
	cases := []struct {
		ev   KeyEvent
		st   State
		want bool
	}{
		{KeyEvent{Key: KeyRune, Rune: 'a', Action: Press}, State{}, false},
		{KeyEvent{Key: KeyRune, Rune: 'a', Mods: ModShift, Action: Press}, State{}, false},
		{KeyEvent{Key: KeyRune, Rune: 'a', Mods: ModAlt, Action: Press}, State{}, false},
		{KeyEvent{Key: KeyRune, Rune: 'a', Mods: ModAlt, Action: Press}, d, true},
		{KeyEvent{Key: KeyRune, Rune: 'a', Mods: ModCtrl, Action: Press}, State{}, true},
		{KeyEvent{Key: KeyKP1, Action: Press}, State{}, false},
		{KeyEvent{Key: KeyKP1, Action: Press}, State{AppKeypad: true}, true},
		{KeyEvent{Key: KeyUp, Action: Press}, State{}, true},
	}
	for _, c := range cases {
		if got := HandledAsKey(c.ev, c.st); got != c.want {
			t.Errorf("%+v %+v: got %v", c.ev, c.st, got)
		}
	}
}

func TestText(t *testing.T) {
	if got := string(EncodeText('é', 0, State{})); got != "é" {
		t.Errorf("got %q", got)
	}
	if got := string(EncodeText('x', ModAlt, State{})); got != "\x1bx" {
		t.Errorf("alt: got %q", got)
	}
}

func TestMouse(t *testing.T) {
	press := MouseEvent{Button: ButtonLeft, Action: MousePress, X: 4, Y: 9}
	release := MouseEvent{Button: ButtonLeft, Action: MouseRelease, X: 4, Y: 9}
	motion := MouseEvent{Button: ButtonNone, Action: MouseMotion, X: 1, Y: 1}
	drag := MouseEvent{Button: ButtonLeft, Action: MouseMotion, X: 2, Y: 3, Mods: ModCtrl}
	wheel := MouseEvent{Button: ButtonWheelDown, Action: MousePress, X: 0, Y: 0}
	tests := []struct {
		ev   MouseEvent
		mode term.MouseMode
		enc  term.MouseEncoding
		want string
	}{
		{press, term.MouseNone, term.MouseEncSGR, ""},
		{press, term.MouseNormal, term.MouseEncSGR, "\x1b[<0;5;10M"},
		{release, term.MouseNormal, term.MouseEncSGR, "\x1b[<0;5;10m"},
		{press, term.MouseNormal, term.MouseEncDefault, "\x1b[M %*"},
		{release, term.MouseNormal, term.MouseEncDefault, "\x1b[M#%*"},
		{release, term.MouseX10, term.MouseEncDefault, ""},
		{motion, term.MouseNormal, term.MouseEncSGR, ""},
		{motion, term.MouseButton, term.MouseEncSGR, ""},
		{motion, term.MouseAny, term.MouseEncSGR, "\x1b[<35;2;2M"},
		{drag, term.MouseButton, term.MouseEncSGR, "\x1b[<48;3;4M"},
		{wheel, term.MouseNormal, term.MouseEncSGR, "\x1b[<65;1;1M"},
		{press, term.MouseNormal, term.MouseEncURXVT, "\x1b[32;5;10M"},
		{MouseEvent{Button: ButtonLeft, Action: MousePress, X: 300, Y: 0}, term.MouseNormal, term.MouseEncDefault, ""},
		{MouseEvent{Button: ButtonLeft, Action: MousePress, X: 300, Y: 0}, term.MouseNormal, term.MouseEncUTF8, "\x1b[M " + string(rune(333)) + "!"},
		{MouseEvent{Button: ButtonLeft, Action: MousePress, PX: 99, PY: 49}, term.MouseNormal, term.MouseEncSGRPixels, "\x1b[<0;100;50M"},
	}
	for _, tt := range tests {
		if got := string(EncodeMouse(tt.ev, tt.mode, tt.enc)); got != tt.want {
			t.Errorf("%+v mode=%d enc=%d: got %q, want %q", tt.ev, tt.mode, tt.enc, got, tt.want)
		}
	}
}

func TestPaste(t *testing.T) {
	if got := string(EncodePaste("a\nb\r\nc", false)); got != "a\rb\rc" {
		t.Errorf("plain: %q", got)
	}
	// An embedded end marker (even one reassembled after removal) must
	// not survive inside the bracketed region.
	got := string(EncodePaste("x\x1b[201~rm -rf ~\x1b[20\x1b[201~1~", true))
	if !strings.HasPrefix(got, "\x1b[200~") || !strings.HasSuffix(got, "\x1b[201~") {
		t.Fatalf("not bracketed: %q", got)
	}
	if inner := got[6 : len(got)-6]; strings.Contains(inner, "\x1b[201~") {
		t.Errorf("end marker survived: %q", got)
	}
}

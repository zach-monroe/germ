package term

import (
	"bytes"
	"strings"
	"testing"
)

func newTerm(cols, rows int) (*Terminal, *bytes.Buffer) {
	var resp bytes.Buffer
	return New(cols, rows, Options{Scrollback: 100, Responder: &resp}), &resp
}

func feed(t *Terminal, s string) { t.Write([]byte(s)) }

func checkText(tb testing.TB, t *Terminal, want ...string) {
	tb.Helper()
	got := t.Text()
	if w := strings.Join(want, "\n"); got != w {
		tb.Errorf("screen mismatch\ngot:\n%s\nwant:\n%s", show(got), show(w))
	}
}

func show(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = "|" + l + "|"
	}
	return strings.Join(lines, "\n")
}

func checkCursor(tb testing.TB, t *Terminal, x, y int) {
	tb.Helper()
	c := t.Cursor()
	if c.X != x || c.Y != y {
		tb.Errorf("cursor = (%d,%d), want (%d,%d)", c.X, c.Y, x, y)
	}
}

func TestPrintAndNewline(t *testing.T) {
	tm, _ := newTerm(10, 3)
	feed(tm, "hello\r\nworld")
	checkText(t, tm, "hello", "world", "")
	checkCursor(t, tm, 5, 1)
}

func TestAutowrapDeferred(t *testing.T) {
	tm, _ := newTerm(5, 3)
	feed(tm, "abcde")
	checkCursor(t, tm, 4, 0) // pending wrap: cursor stays on last column
	feed(tm, "f")
	checkText(t, tm, "abcde", "f", "")
	if !tm.Line(0).Wrapped {
		t.Error("line 0 should be soft-wrapped")
	}

	// Exactly-full line followed by CRLF is a hard break, not a wrap.
	tm2, _ := newTerm(5, 3)
	feed(tm2, "abcde\r\nx")
	checkText(t, tm2, "abcde", "x", "")
	if tm2.Line(0).Wrapped {
		t.Error("CRLF after full line must not mark a soft wrap")
	}
}

func TestNoAutowrap(t *testing.T) {
	tm, _ := newTerm(5, 2)
	feed(tm, "\x1b[?7labcdefgh")
	checkText(t, tm, "abcdh", "")
}

func TestScrollAndScrollback(t *testing.T) {
	tm, _ := newTerm(5, 2)
	feed(tm, "1\r\n2\r\n3\r\n4")
	checkText(t, tm, "3", "4")
	if got := tm.ScrollbackText(); got != "1\n2" {
		t.Errorf("scrollback = %q", got)
	}
}

func TestScrollbackRingRecycles(t *testing.T) {
	tm := New(5, 1, Options{Scrollback: 3})
	for _, s := range []string{"a", "b", "c", "d", "e"} {
		feed(tm, s+"\r\n")
	}
	if got := tm.ScrollbackText(); got != "c\nd\ne" {
		t.Errorf("scrollback = %q", got)
	}
}

func TestCursorMovement(t *testing.T) {
	tm, _ := newTerm(10, 5)
	feed(tm, "\x1b[3;4H")
	checkCursor(t, tm, 3, 2)
	feed(tm, "\x1b[2A")
	checkCursor(t, tm, 3, 0)
	feed(tm, "\x1b[10B")
	checkCursor(t, tm, 3, 4)
	feed(tm, "\x1b[100C")
	checkCursor(t, tm, 9, 4)
	feed(tm, "\x1b[3D")
	checkCursor(t, tm, 6, 4)
	feed(tm, "\x1b[2G")
	checkCursor(t, tm, 1, 4)
	feed(tm, "\x1b[2d")
	checkCursor(t, tm, 1, 1)
	feed(tm, "\x1b[H")
	checkCursor(t, tm, 0, 0)
}

func TestEraseLineAndDisplay(t *testing.T) {
	tm, _ := newTerm(6, 3)
	feed(tm, "aaaaaa\r\nbbbbbb\r\ncccccc")
	feed(tm, "\x1b[2;3H\x1b[K")
	checkText(t, tm, "aaaaaa", "bb", "cccccc")
	feed(tm, "\x1b[1K")
	checkText(t, tm, "aaaaaa", "", "cccccc")
	feed(tm, "\x1b[1;4H\x1b[J")
	checkText(t, tm, "aaa", "", "")
	feed(tm, "\x1b[2J")
	checkText(t, tm, "", "", "")
}

func TestBackgroundColorErase(t *testing.T) {
	tm, _ := newTerm(4, 1)
	feed(tm, "\x1b[41m\x1b[K")
	for x, c := range tm.Line(0).Cells {
		if idx, ok := c.BG.Palette(); !ok || idx != 1 {
			t.Fatalf("cell %d BG = %#x, want palette 1", x, c.BG)
		}
	}
}

func TestInsertDeleteChars(t *testing.T) {
	tm, _ := newTerm(8, 1)
	feed(tm, "abcdef\x1b[1;3H\x1b[2@")
	checkText(t, tm, "ab  cdef")
	feed(tm, "\x1b[3P")
	checkText(t, tm, "abdef")
	feed(tm, "\x1b[2X")
	checkText(t, tm, "ab  f")
	feed(tm, "\x1b[4hXY\x1b[4l")
	checkText(t, tm, "abXY  f")
}

func TestScrollRegion(t *testing.T) {
	tm, _ := newTerm(5, 5)
	feed(tm, "1\r\n2\r\n3\r\n4\r\n5")
	feed(tm, "\x1b[2;4r") // region rows 2-4
	checkCursor(t, tm, 0, 0)
	feed(tm, "\x1b[4;1H\n") // LF at bottom margin scrolls the region only
	checkText(t, tm, "1", "3", "4", "", "5")
	if tm.Scrollback().Len() != 0 {
		t.Error("region not at top must not feed scrollback")
	}
	feed(tm, "\x1b[2;1H\x1bM") // RI at top margin
	checkText(t, tm, "1", "", "3", "4", "5")
	feed(tm, "\x1b[3;1H\x1b[L") // IL
	checkText(t, tm, "1", "", "", "3", "5")
	feed(tm, "\x1b[M") // DL
	checkText(t, tm, "1", "", "3", "", "5")
}

func TestOriginMode(t *testing.T) {
	tm, resp := newTerm(5, 5)
	feed(tm, "\x1b[2;4r\x1b[?6h\x1b[1;1H")
	checkCursor(t, tm, 0, 1)
	feed(tm, "\x1b[10;1H") // clamped to bottom margin
	checkCursor(t, tm, 0, 3)
	feed(tm, "\x1b[6n")
	if got := resp.String(); got != "\x1b[3;1R" {
		t.Errorf("DSR in origin mode = %q", got)
	}
}

func TestSaveRestoreCursor(t *testing.T) {
	tm, _ := newTerm(10, 5)
	feed(tm, "\x1b[3;3H\x1b[1;31m\x1b7\x1b[H\x1b[0m\x1b8x")
	checkCursor(t, tm, 3, 2)
	c := tm.Line(2).Cells[2]
	if c.Attrs&AttrBold == 0 || c.FG != PaletteColor(1) {
		t.Errorf("restored pen not applied: %+v", c)
	}
}

func TestAltScreen(t *testing.T) {
	tm, _ := newTerm(10, 3)
	feed(tm, "shell$ ls\r\nout")
	feed(tm, "\x1b[?1049h")
	checkText(t, tm, "", "", "")
	if !tm.AltScreen() {
		t.Fatal("alt screen not active")
	}
	feed(tm, "\x1b[Hvim stuff\r\n\r\n\r\n\r\nmore") // scrolling in alt: no scrollback
	feed(tm, "\x1b[?1049l")
	checkText(t, tm, "shell$ ls", "out", "")
	checkCursor(t, tm, 3, 1)
	if tm.Scrollback().Len() != 0 {
		t.Errorf("alt screen leaked into scrollback: %q", tm.ScrollbackText())
	}
}

func TestSGR(t *testing.T) {
	tm, _ := newTerm(20, 1)
	feed(tm, "\x1b[1;3;4:3;38;5;196;48;2;1;2;3mA")
	feed(tm, "\x1b[0;38:2::10:20:30;58:5:9;9mB")
	feed(tm, "\x1b[38:2:7:8:9;91;22mC\x1b[mD")
	cs := tm.Line(0).Cells
	a, b, c, d := cs[0], cs[1], cs[2], cs[3]
	if a.Attrs&(AttrBold|AttrItalic) != AttrBold|AttrItalic || a.Underline != UnderlineCurly {
		t.Errorf("A attrs: %+v", a)
	}
	if a.FG != PaletteColor(196) || a.BG != RGBColor(1, 2, 3) {
		t.Errorf("A colors: fg=%#x bg=%#x", a.FG, a.BG)
	}
	if b.Attrs != AttrStrike || b.FG != RGBColor(10, 20, 30) || b.UL != PaletteColor(9) || !b.BG.IsDefault() {
		t.Errorf("B: %+v", b)
	}
	if c.FG != PaletteColor(9) || c.Attrs&AttrBold != 0 {
		t.Errorf("C: %+v (91 should override 38:2 that came first)", c)
	}
	if d != (Cell{Rune: 'D'}) {
		t.Errorf("D after reset: %+v", d)
	}
}

func TestWideChars(t *testing.T) {
	tm, _ := newTerm(5, 2)
	feed(tm, "a世b")
	checkText(t, tm, "a世b", "")
	checkCursor(t, tm, 4, 0)
	cs := tm.Line(0).Cells
	if cs[1].Attrs&AttrWide == 0 || cs[2].Attrs&AttrWideSpacer == 0 {
		t.Errorf("wide flags: %+v %+v", cs[1], cs[2])
	}
	// Overwriting the spacer half erases the whole wide char.
	feed(tm, "\x1b[1;3Hx")
	checkText(t, tm, "a xb", "")

	// A wide char that doesn't fit wraps early, leaving padding.
	tm2, _ := newTerm(5, 2)
	feed(tm2, "abcd世")
	checkText(t, tm2, "abcd", "世")
}

func TestTabs(t *testing.T) {
	tm, _ := newTerm(20, 1)
	feed(tm, "a\tb")
	checkCursor(t, tm, 9, 0)
	feed(tm, "\x1b[3g\r\x1b[5C\x1bH\r\tX")
	checkText(t, tm, "a    X  b")
	feed(tm, "\x1b[Z")
	checkCursor(t, tm, 5, 0)
}

func TestDECGraphics(t *testing.T) {
	tm, _ := newTerm(10, 1)
	feed(tm, "\x1b(0lqk\x1b(Bx")
	checkText(t, tm, "┌─┐x")
	feed(tm, "\r\x1b)0\x0eq\x0fq")
	checkText(t, tm, "─q┐x")
}

func TestREP(t *testing.T) {
	tm, _ := newTerm(10, 1)
	feed(tm, "x\x1b[4b")
	checkText(t, tm, "xxxxx")
}

func TestResponses(t *testing.T) {
	tests := []struct{ in, want string }{
		{"\x1b[c", "\x1b[?62;22c"},
		{"\x1b[>c", "\x1b[>1;100;0c"},
		{"\x1b[5n", "\x1b[0n"},
		{"\x1b[2;3H\x1b[6n", "\x1b[2;3R"},
		{"\x1b[?2026$p", "\x1b[?2026;2$y"},
		{"\x1b[?2026h\x1b[?2026$p", "\x1b[?2026;1$y"},
		{"\x1b[?9999$p", "\x1b[?9999;0$y"},
		{"\x1b[4$p", "\x1b[4;2$y"},
		{"\x1b[>q", "\x1bP>|germ " + Version + "\x1b\\"},
		{"\x1b[18t", "\x1b[8;24;80t"},
		{"\x1b[?u", ""}, // kitty keyboard: not yet supported
	}
	for _, tt := range tests {
		tm, resp := newTerm(80, 24)
		feed(tm, tt.in)
		if got := resp.String(); got != tt.want {
			t.Errorf("%q: got %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestModes(t *testing.T) {
	tm, _ := newTerm(10, 2)
	feed(tm, "\x1b[?1h\x1b[?25l\x1b[?2004h\x1b[?1002h\x1b[?1006h\x1b[?1004h\x1b[5 q")
	m := tm.Modes()
	if !m.AppCursor || m.CursorVisible || !m.BracketedPaste || m.Mouse != MouseButton ||
		m.MouseEncoding != MouseEncSGR || !m.FocusEvents {
		t.Errorf("modes: %+v", m)
	}
	if tm.CursorStyle() != CursorBlinkBar {
		t.Errorf("cursor style = %v", tm.CursorStyle())
	}
	feed(tm, "\x1b[?1002l\x1b[?25h")
	if m := tm.Modes(); m.Mouse != MouseNone || !m.CursorVisible {
		t.Errorf("modes after reset: %+v", m)
	}
}

func TestTitle(t *testing.T) {
	var got string
	tm := New(10, 1, Options{OnTitle: func(s string) { got = s }})
	feed(tm, "\x1b]2;~/germ — fish\x07")
	if got != "~/germ — fish" || tm.Title() != got {
		t.Errorf("title = %q", got)
	}
}

func TestFullReset(t *testing.T) {
	tm, _ := newTerm(5, 2)
	feed(tm, "ab\x1b[1m\x1b[?7l\x1bc")
	checkText(t, tm, "", "")
	checkCursor(t, tm, 0, 0)
	if !tm.Modes().Autowrap || tm.Cursor().Pen != (Cell{}) {
		t.Error("RIS did not reset state")
	}
}

func TestResizeReflowNarrower(t *testing.T) {
	tm, _ := newTerm(10, 3)
	feed(tm, "0123456789abc\r\nxy")
	checkText(t, tm, "0123456789", "abc", "xy")
	tm.Resize(5, 3)
	// "0123456789abc" now takes 3 rows; one row goes to scrollback.
	checkText(t, tm, "56789", "abc", "xy")
	if got := tm.ScrollbackText(); got != "01234" {
		t.Errorf("scrollback = %q", got)
	}
	checkCursor(t, tm, 2, 2)
}

func TestResizeReflowWider(t *testing.T) {
	tm, _ := newTerm(5, 3)
	feed(tm, "0123456789ab\r\n$ ")
	tm.Resize(20, 3)
	checkText(t, tm, "0123456789ab", "$", "")
	checkCursor(t, tm, 2, 1)
	if tm.Scrollback().Len() != 0 {
		t.Errorf("scrollback should be pulled back: %q", tm.ScrollbackText())
	}
}

func TestResizeTallerPullsScrollback(t *testing.T) {
	tm, _ := newTerm(5, 2)
	feed(tm, "a\r\nb\r\nc\r\nd")
	tm.Resize(5, 4)
	checkText(t, tm, "a", "b", "c", "d")
	checkCursor(t, tm, 1, 3)
}

func TestResizeShorterKeepsCursorVisible(t *testing.T) {
	tm, _ := newTerm(5, 4)
	feed(tm, "a\r\nb\r\nc")
	tm.Resize(5, 2)
	checkText(t, tm, "b", "c")
	checkCursor(t, tm, 1, 1)
}

func TestResizeAltScreen(t *testing.T) {
	tm, _ := newTerm(10, 3)
	feed(tm, "prompt\r\n\x1b[?1049h\x1b[Hfull screen app")
	tm.Resize(4, 2)
	checkText(t, tm, "full", "n ap") // "full scree" / "n app", truncated
	feed(tm, "\x1b[?1049l")
	// The primary screen reflowed underneath; its saved cursor followed.
	checkText(t, tm, "pt", "")
	checkCursor(t, tm, 0, 1)
	if got := tm.ScrollbackText(); got != "prom" {
		t.Errorf("scrollback = %q", got)
	}
}

func TestResizeWideChars(t *testing.T) {
	tm, _ := newTerm(6, 2)
	feed(tm, "ab世界")
	tm.Resize(5, 2)
	checkText(t, tm, "ab世", "界")
	tm.Resize(6, 2)
	checkText(t, tm, "ab世界", "")
}

func BenchmarkThroughputASCII(b *testing.B) {
	line := strings.Repeat("the quick brown fox jumps over the lazy dog ", 2) + "\r\n"
	in := []byte(strings.Repeat(line, (1<<20)/len(line)))
	tm := New(120, 40, Options{Scrollback: 10000})
	b.SetBytes(int64(len(in)))
	for b.Loop() {
		tm.Write(in)
	}
}

func BenchmarkThroughputSGR(b *testing.B) {
	line := "\x1b[1;32mfile.go\x1b[0m  \x1b[38;2;10;20;30mrgb text\x1b[m 世界 plain text\r\n"
	in := []byte(strings.Repeat(line, (1<<20)/len(line)))
	tm := New(120, 40, Options{Scrollback: 10000})
	b.SetBytes(int64(len(in)))
	for b.Loop() {
		tm.Write(in)
	}
}

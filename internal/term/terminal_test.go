package term

import (
	"bytes"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/zach-monroe/germ/internal/theme"
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
		{"\x1b[?2026h\x1b[?2026$p\x1b[?2026l", "\x1b[?2026;1$y"},
		{"\x1b[?9999$p", "\x1b[?9999;0$y"},
		{"\x1b[4$p", "\x1b[4;2$y"},
		{"\x1b[>q", "\x1bP>|germ " + Version + "\x1b\\"},
		{"\x1b[18t", "\x1b[8;24;80t"},
		{"\x1b[?u", "\x1b[?0u"},
		{"\x1b[>11u\x1b[?u", "\x1b[?11u"},
		{"\x1b[>1u\x1b[>5u\x1b[<u\x1b[?u", "\x1b[?1u"}, // 4 is unsupported: masked
		{"\x1b[=1u\x1b[=2;2u\x1b[?u", "\x1b[?3u"},
		{"\x1b[=3u\x1b[=1;3u\x1b[?u", "\x1b[?2u"},
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

func TestKittyFlagsPerScreen(t *testing.T) {
	tm, _ := newTerm(10, 3)
	feed(tm, "\x1b[>1u\x1b[?1049h")
	if f := tm.KeyboardFlags(); f != 0 {
		t.Errorf("alt screen inherited flags %d", f)
	}
	feed(tm, "\x1b[>8u\x1b[?1049l")
	if f := tm.KeyboardFlags(); f != 1 {
		t.Errorf("primary flags = %d, want 1", f)
	}
}

func TestAbsRowsAndTextRange(t *testing.T) {
	tm, _ := newTerm(5, 2)
	feed(tm, "one\r\ntwo\r\nthreefour\r\nx")
	// scrollback: one, two, three(wrapped) ; screen: four, x
	tm.Lock()
	defer tm.Unlock()
	if tm.ScrollbackLen() != 3 || tm.Pushed() != 3 {
		t.Fatalf("sb=%d pushed=%d", tm.ScrollbackLen(), tm.Pushed())
	}
	if l := tm.LineAt(tm.AbsRow(0, 3)); l == nil || l.Cells[0].Rune != 'o' {
		t.Errorf("top of fully scrolled view should be 'one'")
	}
	got := tm.TextRange(1, 1, 4, 0) // "wo" .. "x"
	if want := "wo\nthreefour\nx"; got != want {
		t.Errorf("TextRange = %q, want %q", got, want)
	}
	if tm.LineAt(-1) != nil || tm.LineAt(99) != nil {
		t.Error("out-of-range rows should be nil")
	}
}

func TestOSCColors(t *testing.T) {
	tm, resp := newTerm(10, 2)
	th, _ := tm.Colors()
	bg := th.Background
	feed(tm, "\x1b]11;?\x07")
	want := fmt.Sprintf("\x1b]11;rgb:%02x%02x/%02x%02x/%02x%02x\x1b\\", bg.R, bg.R, bg.G, bg.G, bg.B, bg.B)
	if resp.String() != want {
		t.Errorf("OSC 11 query = %q, want %q", resp.String(), want)
	}
	resp.Reset()
	_, gen := tm.Colors()
	feed(tm, "\x1b]4;1;#102030\x1b\\\x1b]4;1;?\x1b\\")
	if resp.String() != "\x1b]4;1;rgb:1010/2020/3030\x1b\\" {
		t.Errorf("OSC 4 = %q", resp.String())
	}
	th2, gen2 := tm.Colors()
	if gen2 == gen || th2.Palette[1].Hex() != "#102030" {
		t.Error("palette override not applied")
	}
	feed(tm, "\x1b]10;rgb:ff/00/00;#00ff00\x07") // sets fg and bg
	if th3, _ := tm.Colors(); th3.Foreground.Hex() != "#ff0000" || th3.Background.Hex() != "#00ff00" {
		t.Errorf("OSC 10 multi: fg=%s bg=%s", th3.Foreground.Hex(), th3.Background.Hex())
	}
	// Overrides survive a theme change; resets restore the theme.
	tm.SetTheme(theme.Default())
	if th4, _ := tm.Colors(); th4.Palette[1].Hex() != "#102030" {
		t.Error("override lost on SetTheme")
	}
	feed(tm, "\x1b]104\x07\x1b]110\x07\x1b]111\x07")
	if th5, _ := tm.Colors(); *th5 != *theme.Default() {
		t.Error("resets did not restore theme")
	}
}

func TestFixedColors(t *testing.T) {
	tm, resp := newTerm(10, 2)
	feed(tm, "\x1b]4;1;#102030\x07\x1b]11;#000000\x07")
	tm.SetFixedColors(true) // drops what was set
	if th, _ := tm.Colors(); *th != *theme.Default() {
		t.Error("turning fixed on kept a program's colors")
	}
	feed(tm, "\x1b]4;1;#102030\x07\x1b]10;#ff0000;#00ff00\x07\x1b]4;1;?\x07")
	if th, _ := tm.Colors(); *th != *theme.Default() {
		t.Error("fixed colors changed")
	}
	if want := "\x1b]4;1;" + rgbSpec(theme.Default().Palette[1]) + "\x1b\\"; resp.String() != want {
		t.Errorf("query = %q, want %q", resp.String(), want)
	}
	tm.SetFixedColors(false)
	feed(tm, "\x1b]4;1;#102030\x07")
	if th, _ := tm.Colors(); th.Palette[1].Hex() != "#102030" {
		t.Error("not fixed any more, but the change was ignored")
	}
}

func TestShellIntegrationMarks(t *testing.T) {
	var done []int
	tm := New(20, 4, Options{Scrollback: 100, OnCommandDone: func(exit int, _ time.Duration) { done = append(done, exit) }})
	feed(tm, "\x1b]7;file://host/home/zach/my%20dir\x07")
	feed(tm, "\x1b]133;A\x07$ \x1b]133;B\x07ls\r\n\x1b]133;C\x07a\r\nb\r\n\x1b]133;D;2\x07")
	feed(tm, "\x1b]133;A\x07$ ")
	tm.Lock()
	defer tm.Unlock()
	if tm.CWD() != "/home/zach/my dir" {
		t.Errorf("cwd = %q", tm.CWD())
	}
	sr, sc, er, ec, ok := tm.LastOutput()
	if !ok || tm.TextRange(sr, sc, er, ec) != "a\nb" {
		t.Errorf("last output = %q (ok=%v)", tm.TextRange(sr, sc, er, ec), ok)
	}
	if l := tm.Line(0); !l.Prompt || !l.HasExit || l.Exit != 2 {
		t.Errorf("prompt line marks: %+v", l)
	}
	if l := tm.Line(3); !l.Prompt || l.HasExit {
		t.Errorf("second prompt marks: prompt=%v exit=%v", l.Prompt, l.HasExit)
	}
	if len(done) != 1 || done[0] != 2 {
		t.Errorf("OnCommandDone calls = %v", done)
	}
}

func TestPromptMarksSurviveScrollAndReflow(t *testing.T) {
	tm, _ := newTerm(10, 3)
	feed(tm, "\x1b]133;A\x07$ cmd\r\nout1\r\nout2\r\nout3\r\n")
	tm.Lock()
	if l := tm.LineAt(0); l == nil || !l.Prompt {
		t.Error("prompt mark lost when scrolled into scrollback")
	}
	tm.Unlock()
	tm.Resize(4, 3)
	tm.Lock()
	defer tm.Unlock()
	found := 0
	for row := tm.AbsRow(0, tm.ScrollbackLen()); row < tm.AbsRow(3, 0); row++ {
		if l := tm.LineAt(row); l != nil && l.Prompt {
			found++
		}
	}
	if found != 1 {
		t.Errorf("after reflow found %d prompt marks, want 1", found)
	}
}

func TestOSC52(t *testing.T) {
	var got []string
	tm := New(10, 2, Options{OnClipboard: func(s string, c, p bool) { got = append(got, fmt.Sprintf("%s|%v|%v", s, c, p)) }})
	feed(tm, "\x1b]52;c;aGVsbG8=\x07\x1b]52;p;d29ybGQ=\x1b\\\x1b]52;c;?\x07\x1b]52;c;!!!\x07")
	want := []string{"hello|true|false", "world|false|true"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestDECRQSSAndXTGETTCAP(t *testing.T) {
	tm, resp := newTerm(10, 5)
	feed(tm, "\x1b[1;4:3;38;2;1;2;3;41m\x1bP$qm\x1b\\")
	if got := resp.String(); got != "\x1bP1$r0;1;4:3;38:2::1:2:3;41m\x1b\\" {
		t.Errorf("DECRQSS m = %q", got)
	}
	resp.Reset()
	feed(tm, "\x1b[2;4r\x1bP$qr\x1b\\\x1bP$qx\x1b\\")
	if got := resp.String(); got != "\x1bP1$r2;4r\x1b\\\x1bP0$r\x1b\\" {
		t.Errorf("DECRQSS r/bad = %q", got)
	}
	resp.Reset()
	// "TN" = 544e, "RGB" = 524742, "Tc" = 5463, "xx" = 7878
	feed(tm, "\x1bP+q544e;524742;5463;7878\x1b\\")
	want := "\x1bP1+r544e=6765726D\x1b\\\x1bP1+r524742=382F382F38\x1b\\\x1bP1+r5463\x1b\\\x1bP0+r7878\x1b\\"
	if got := resp.String(); got != want {
		t.Errorf("XTGETTCAP = %q\nwant        %q", got, want)
	}
}

func TestModifyOtherKeys(t *testing.T) {
	tm, resp := newTerm(10, 2)
	feed(tm, "\x1b[>4;2m\x1b[?4m")
	if tm.ModifyOtherKeys() != 2 || resp.String() != "\x1b[>4;2m" {
		t.Errorf("level=%d resp=%q", tm.ModifyOtherKeys(), resp.String())
	}
	feed(tm, "\x1b[>4n")
	if tm.ModifyOtherKeys() != 0 {
		t.Error("reset failed")
	}
}

func TestNotifications(t *testing.T) {
	type note struct{ title, body string }
	var got []note
	var prog [][2]int
	term := New(20, 5, Options{
		OnNotify:   func(title, body string) { got = append(got, note{title, body}) },
		OnProgress: func(s, p int) { prog = append(prog, [2]int{s, p}) },
	})
	term.Write([]byte("\x1b]9;Claude needs your permission\x07"))
	term.Write([]byte("\x1b]777;notify;Codex;Task done\x1b\\"))
	term.Write([]byte("\x1b]99;i=1:d=0;Hello\x1b\\\x1b]99;i=1:p=body;World\x1b\\"))
	term.Write([]byte("\x1b]9;4;3;0\x07\x1b]9;4;1;250\x07"))
	want := []note{{"", "Claude needs your permission"}, {"Codex", "Task done"}, {"Hello", "World"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("notes %q", got)
	}
	if !reflect.DeepEqual(prog, [][2]int{{3, 0}, {1, 100}}) {
		t.Errorf("progress %v", prog)
	}
}

func TestHyperlinks(t *testing.T) {
	term := New(30, 3, Options{})
	term.Write([]byte("see \x1b]8;id=a;https://example.com/x\x1b\\here\x1b[0m!\x1b]8;;\x1b\\ done"))
	term.Write([]byte("\r\n\x1b]8;;javascript:alert(1)\x07bad\x1b]8;;\x07"))
	term.Lock()
	defer term.Unlock()
	l := term.LineAt(term.AbsRow(0, 0))
	if id := l.Cells[4].Link; id == 0 || term.LinkURL(id) != "https://example.com/x" {
		t.Fatalf("cell 4 link %d %q", id, term.LinkURL(id))
	}
	if l.Cells[8].Link != l.Cells[4].Link { // "!" after an SGR reset is still linked
		t.Error("SGR reset ended the link")
	}
	if l.Cells[3].Link != 0 || l.Cells[10].Link != 0 {
		t.Error("link leaked outside its text")
	}
	if term.LineAt(term.AbsRow(1, 0)).Cells[0].Link != 0 {
		t.Error("javascript: link accepted")
	}
}

func TestActionLinksOnlyWhenAllowed(t *testing.T) {
	const out = "\x1b]8;;germ-widget:tick\x1b\\a\x1b]8;;\x1b\\ \x1b]8;;germ-open:/tmp\x1b\\b\x1b]8;;\x1b\\"
	for _, allow := range []bool{false, true} {
		term := New(10, 1, Options{ActionLinks: allow})
		term.Write([]byte(out))
		term.Lock()
		l := term.LineAt(term.AbsRow(0, 0))
		got := [2]string{term.LinkURL(l.Cells[0].Link), term.LinkURL(l.Cells[2].Link)}
		term.Unlock()
		want := [2]string{}
		if allow {
			want = [2]string{"germ-widget:tick", "germ-open:/tmp"}
		}
		if l.Cells[0].Link == 0 {
			got[0] = ""
		}
		if l.Cells[2].Link == 0 {
			got[1] = ""
		}
		if got != want {
			t.Errorf("ActionLinks=%v: links %q, want %q", allow, got, want)
		}
	}
}

func TestCombining(t *testing.T) {
	term := New(20, 2, Options{})
	// e + acute composes; q + tilde has no precomposed form and becomes a
	// mark; ZWJ is dropped.
	term.Write([]byte("e\u0301x q\u0303 a\u200d"))
	term.Lock()
	l := term.LineAt(term.AbsRow(0, 0))
	if l.Cells[0].Rune != '\u00e9' || l.Cells[1].Rune != 'x' {
		t.Errorf("cells %q %q", l.Cells[0].Rune, l.Cells[1].Rune)
	}
	if l.Cells[3].Rune != 'q' || l.Cells[3].Mark != '\u0303' {
		t.Errorf("mark cell %q %q", l.Cells[3].Rune, l.Cells[3].Mark)
	}
	term.Unlock()
	if got := term.Text(); !strings.HasPrefix(got, "\u00e9x q\u0303 a") {
		t.Errorf("text %q", got)
	}
}

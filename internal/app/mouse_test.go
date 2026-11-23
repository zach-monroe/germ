package app

import (
	"testing"

	"github.com/zach-monroe/germ/internal/term"
)

func newTerm(t *testing.T, cols, rows int, content string) *term.Terminal {
	t.Helper()
	tm := term.New(cols, rows, term.Options{Scrollback: 100})
	tm.Write([]byte(content))
	return tm
}

func TestWordSelection(t *testing.T) {
	tm := newTerm(t, 40, 2, "cat ~/germ/PLAN.md (hello) end")
	s := selection{active: true, mode: selWord, anchor: selPoint{0, 6}, head: selPoint{0, 6}}
	if got := s.text(tm); got != "~/germ/PLAN.md" {
		t.Errorf("word = %q", got)
	}
	s.anchor, s.head = selPoint{0, 21}, selPoint{0, 21}
	if got := s.text(tm); got != "hello" {
		t.Errorf("word in parens = %q", got)
	}
}

func TestLineSelectionFollowsWraps(t *testing.T) {
	tm := newTerm(t, 5, 4, "abcdefgh\r\nnext")
	s := selection{active: true, mode: selLine, anchor: selPoint{1, 1}, head: selPoint{1, 1}}
	if got := s.text(tm); got != "abcdefgh" {
		t.Errorf("line = %q", got)
	}
}

func TestCharSelectionReversed(t *testing.T) {
	tm := newTerm(t, 10, 3, "hello\r\nworld")
	s := selection{active: true, anchor: selPoint{1, 2}, head: selPoint{0, 3}}
	if got := s.text(tm); got != "lo\nwor" {
		t.Errorf("text = %q", got)
	}
}

func TestFindURL(t *testing.T) {
	tm := newTerm(t, 30, 3, "see https://example.com/a_(b) and https://go.dev/x.")
	tm.Lock()
	defer tm.Unlock()

	url, link := findURL(tm, 0, 10)
	if url != "https://example.com/a_(b)" || link.StartCol != 4 || !link.Active {
		t.Errorf("first: %q %+v", url, link)
	}
	url, link = findURL(tm, 1, 6) // trailing '.' is excluded
	if url != "https://go.dev/x" || link.StartCol != 4 || link.EndCol != 19 {
		t.Errorf("second: %q %+v", url, link)
	}
	if url, _ := findURL(tm, 0, 0); url != "" {
		t.Errorf("no URL at col 0, got %q", url)
	}
}

func TestFindURLWrapped(t *testing.T) {
	tm := newTerm(t, 12, 3, "x https://go.dev/abc.")
	tm.Lock()
	defer tm.Unlock()
	// Row 0: "x https://go"  Row 1: ".dev/abc."
	url, link := findURL(tm, 1, 2)
	if url != "https://go.dev/abc" {
		t.Fatalf("url = %q", url)
	}
	if link.Row != 1 || link.StartCol != 0 || link.EndCol != 7 {
		t.Errorf("highlight on row 1 = %+v, want cols 0-7", link)
	}
	if _, l0 := findURL(tm, 0, 5); l0.StartCol != 2 || l0.EndCol < 11 {
		t.Errorf("highlight on row 0 = %+v", l0)
	}
}

func TestJumpPrompt(t *testing.T) {
	tm := term.New(10, 3, term.Options{Scrollback: 100})
	for _, cmd := range []string{"a", "b", "c"} {
		tm.Write([]byte("\x1b]133;A\x07$ " + cmd + "\r\nout\r\nout\r\n"))
	}
	// Rows: 0 "$ a", 3 "$ b", 6 "$ c", 9 (cursor, live top is row 7).
	a := &App{term: tm}
	a.jumpPrompt(-1)
	tm.Lock()
	top := tm.AbsRow(0, a.offset)
	tm.Unlock()
	if top != 6 {
		t.Fatalf("first jump: top row %d, want 6", top)
	}
	a.jumpPrompt(-1)
	a.jumpPrompt(-1)
	tm.Lock()
	top = tm.AbsRow(0, a.offset)
	tm.Unlock()
	if top != 0 {
		t.Fatalf("third jump: top row %d, want 0", top)
	}
	a.jumpPrompt(1)
	tm.Lock()
	top = tm.AbsRow(0, a.offset)
	tm.Unlock()
	if top != 3 {
		t.Errorf("jump forward: top row %d, want 3", top)
	}
}

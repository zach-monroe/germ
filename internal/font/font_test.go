package font

import (
	"os/exec"
	"testing"
)

func load(t *testing.T) *Set {
	t.Helper()
	if _, err := exec.LookPath("fc-match"); err != nil {
		t.Skip("fontconfig not installed")
	}
	s, err := Load("monospace", 12, 96)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestMetrics(t *testing.T) {
	m := load(t).Metrics()
	if m.CellW < 5 || m.CellH < 10 || m.CellH < m.CellW {
		t.Errorf("implausible metrics: %+v", m)
	}
	if m.Baseline <= 0 || m.Baseline >= m.CellH {
		t.Errorf("baseline out of cell: %+v", m)
	}
}

func TestGlyph(t *testing.T) {
	s := load(t)
	m := s.Metrics()
	g, ok := s.Glyph('M', Regular)
	if !ok {
		t.Fatal("no glyph for M")
	}
	if g.Bounds.Dx() > m.CellW+2 || g.Bounds.Max.Y > m.CellH+2 || g.Bounds.Min.Y < -2 {
		t.Errorf("M bounds %v outside cell %dx%d", g.Bounds, m.CellW, m.CellH)
	}
	var ink int
	for _, a := range g.Mask.Pix {
		ink += int(a)
	}
	if ink == 0 {
		t.Error("M rendered empty")
	}
	if _, ok := s.Glyph(' ', Regular); ok {
		t.Error("space should have no ink")
	}
	if _, ok := s.Glyph('A', Bold); !ok {
		t.Error("no bold A")
	}
}

func TestFallback(t *testing.T) {
	s := load(t)
	if _, ok := s.Glyph('世', Regular); !ok {
		t.Skip("no CJK font installed")
	}
	if _, ok := s.Glyph(0x10FFFD, Regular); ok { // private use, unassigned
		t.Error("expected no glyph for U+10FFFD")
	}
}

func TestBoxDrawingFillsCell(t *testing.T) {
	s := load(t)
	m := s.Metrics()
	g, ok := s.Glyph('─', Regular)
	if !ok || g.Bounds.Dx() != m.CellW || g.Bounds.Dy() != m.CellH {
		t.Fatalf("─ bounds %v, want full cell %dx%d", g.Bounds, m.CellW, m.CellH)
	}
	// A horizontal line must touch both cell edges so adjacent cells join.
	row := -1
	for y := 0; y < m.CellH; y++ {
		if g.Mask.Pix[y*g.Mask.Stride] != 0 {
			row = y
			break
		}
	}
	if row < 0 || g.Mask.Pix[row*g.Mask.Stride+m.CellW-1] == 0 {
		t.Errorf("─ does not span the cell (row %d)", row)
	}
	full, _ := s.Glyph('█', Regular)
	for _, a := range full.Mask.Pix {
		if a != 0xff {
			t.Fatal("█ not solid")
		}
	}
}

func TestNerdFontIcons(t *testing.T) {
	if _, err := exec.LookPath("fc-match"); err != nil {
		t.Skip("fontconfig not installed")
	}
	s, err := Load("JetBrainsMono Nerd Font", 10, 96)
	if err != nil {
		t.Skip("JetBrainsMono Nerd Font not installed")
	}
	notdef, _ := s.Glyph(0x10FFFD, Regular)
	for _, r := range []rune{0xF303 /* arch */, 0xE626 /* go */, 0xF418 /* branch */} {
		g, ok := s.Glyph(r, Regular)
		if !ok {
			t.Errorf("U+%X: no glyph", r)
			continue
		}
		if notdef.Mask != nil && string(g.Mask.Pix) == string(notdef.Mask.Pix) {
			t.Errorf("U+%X rendered as .notdef", r)
		}
	}
}

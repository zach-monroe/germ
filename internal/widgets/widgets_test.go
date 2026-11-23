package widgets

import (
	"strings"
	"testing"
)

func TestWidth(t *testing.T) {
	if n := width("\x1b[1;31mhé\x1b[0m!"); n != 3 {
		t.Errorf("width = %d", n)
	}
}

func TestWrap(t *testing.T) {
	got := wrap("the quick brown fox jumps over the lazy dog", 10, 2)
	if len(got) != 2 || got[0] != "the quick" || !strings.HasSuffix(got[1], "…") {
		t.Errorf("wrap = %q", got)
	}
	if got := wrap("short", 10, 3); len(got) != 1 || got[0] != "short" {
		t.Errorf("wrap = %q", got)
	}
}

func TestSparkline(t *testing.T) {
	if s := sparkline([]float64{1, 2, 3, 4}, 4); s != "▁▃▅█" {
		t.Errorf("sparkline = %q", s)
	}
	if s := sparkline([]float64{5, 5}, 3); s != "▁▁▁" {
		t.Errorf("flat sparkline = %q", s)
	}
}

func TestBigText(t *testing.T) {
	full := bigText("1", 1, false)
	if len(full) != 5 || full[0] != " █ " {
		t.Errorf("full = %q", full)
	}
	half := bigText("1", 1, true)
	if len(half) != 3 || half[0] != "▄█ " {
		t.Errorf("half = %q", half)
	}
}

func TestDrawFrames(t *testing.T) {
	// Every built-in must draw without data, and fit boxes of usable size
	// (draw clips anything taller; tiny boxes just mustn't panic).
	for _, name := range Names() {
		w := builtins[name]()
		for _, sz := range [][2]int{{80, 20}, {20, 4}, {5, 2}} {
			lines := w.Draw(sz[0], sz[1], nil)
			if sz[1] >= 4 && len(lines) > sz[1] {
				t.Errorf("%s %v: %d lines", name, sz, len(lines))
			}
		}
	}
}

func TestWidthSkipsHyperlinks(t *testing.T) {
	if n := width(link("https://example.com", "abc") + "d"); n != 4 {
		t.Errorf("width = %d", n)
	}
}

func TestTimeline(t *testing.T) {
	hist := strings.Repeat(" ", 168) + "wwwwww" + "iiii!i" // last two minutes
	got := width(timeline(hist, 3))
	if got != 3 {
		t.Errorf("width %d", got)
	}
	s := timeline(hist, 2)
	if !strings.Contains(s, "▅") || !strings.Contains(s, "▇") {
		t.Errorf("timeline %q", s)
	}
}

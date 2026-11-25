package app

import "testing"

func TestFuzzyScore(t *testing.T) {
	if fuzzyScore("Split down", []rune("sd")) < 0 || fuzzyScore("Split down", []rune("xyz")) >= 0 {
		t.Error("match/no match")
	}
	// Word starts beat scattered letters.
	if !(fuzzyScore("New tab", []rune("nt")) < fuzzyScore("Focus pane to the left", []rune("nt"))) {
		t.Error("word starts should score better")
	}
	if fuzzyScore("Search scrollback", []rune("SEARCH")) != fuzzyScore("Search scrollback", []rune("search")) {
		t.Error("case should not matter")
	}
}

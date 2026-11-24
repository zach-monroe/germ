package app

import (
	"fmt"
	"testing"

	"github.com/zach-monroe/germ/internal/term"
)

func TestFindAll(t *testing.T) {
	tm := term.New(10, 3, term.Options{Scrollback: 100})
	for i := 0; i < 20; i++ {
		fmt.Fprintf(tm, "line %d\r\n", i)
	}
	tm.Write([]byte("an Error and an error\r\n")) // wraps at 10 columns
	tm.Write([]byte("ok"))

	ms, _ := findAll(tm, "line 1")
	if len(ms) != 11 { // line 1, line 10..19
		t.Errorf("'line 1': %d matches", len(ms))
	}
	if ms[0].startRow >= ms[1].startRow {
		t.Error("matches not oldest first")
	}

	if ms, _ := findAll(tm, "error"); len(ms) != 2 {
		t.Errorf("smart case lower: %d matches, want 2", len(ms))
	}
	ms, _ = findAll(tm, "Error")
	if len(ms) != 1 {
		t.Fatalf("smart case upper: %d matches, want 1", len(ms))
	}
	// "an Error a|nd an err|or": the second "error" crosses a wrap.
	all, _ := findAll(tm, "error")
	last := all[len(all)-1]
	if last.endRow != last.startRow+1 {
		t.Errorf("wrapped match rows %d..%d", last.startRow, last.endRow)
	}
	if ms, _ := findAll(tm, "nope"); len(ms) != 0 {
		t.Error("found a missing string")
	}
}

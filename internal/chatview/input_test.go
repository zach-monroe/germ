package chatview

import (
	"strings"
	"testing"
)

func TestInput(t *testing.T) {
	var in Input
	in.Insert("hello world")
	in.Left()
	in.Left()
	in.Backspace() // hello wold
	in.End()
	in.DeleteWord() // "hello "
	if got := string(in.Text); got != "hello " || in.Cur != 6 {
		t.Fatalf("edit: %q cur %d", got, in.Cur)
	}
	in.Insert("\tthere\r\nok\x07") // tab → space, CRLF → LF, BEL dropped
	if got := string(in.Text); got != "hello  there\nok" {
		t.Fatalf("insert: %q", got)
	}
	in.Home()
	in.Right()
	in.KillToEnd()
	in.Delete() // nothing after the cursor
	if got := string(in.Text); got != "h" {
		t.Fatalf("kill: %q", got)
	}
	in.Clear()
	if in.Insert(strings.Repeat("x", MaxInput+1)) || !in.Empty() {
		t.Fatal("over-long insert accepted")
	}

	// Sent history: Up from an empty box recalls, Down goes back to empty.
	in.Insert("  first ")
	if got := in.Take(); got != "first" || !in.Empty() {
		t.Fatalf("take: %q", got)
	}
	in.Insert("second")
	in.Take()
	if in.Take() != "" {
		t.Fatal("blank take")
	}
	in.Up()
	in.Up()
	in.Up() // stays on the oldest
	if string(in.Text) != "first" || !in.Recalling() {
		t.Fatalf("up: %q", string(in.Text))
	}
	in.Down()
	in.Down()
	if !in.Empty() || in.Recalling() {
		t.Fatalf("down: %q", string(in.Text))
	}
	in.Insert("draft")
	if in.CanRecall() {
		t.Fatal("Up would replace a draft")
	}
}

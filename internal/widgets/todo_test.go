package widgets

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// tokenFor finds the click token of the drawn row containing text.
func tokenFor(t *testing.T, lines []string, text string) string {
	t.Helper()
	for _, l := range lines {
		if strings.Contains(l, text) {
			if m := regexp.MustCompile(`germ-widget:([^\x1b]+)\x1b`).FindStringSubmatch(l); m != nil {
				return m[1]
			}
		}
	}
	t.Fatalf("no clickable row with %q in %q", text, lines)
	return ""
}

func TestTodo(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "dotfiles", "todo.md")
	os.MkdirAll(filepath.Dir(real), 0o755)
	body := "# Todo\n\nSome notes that stay put.\n\n## Work\n- [ ] fix the tests\n  - [x] find the flake\n- [ ] ship it\n\n## Empty\n\n## Home\n* [X] buy milk\n1. [ ] call mom\n"
	os.WriteFile(real, []byte(body), 0o600)
	link := filepath.Join(dir, "todo.md")
	os.Symlink(real, link)

	td := &todo{path: link}
	ctx := context.Background()
	td.Update(ctx)
	lines := td.Draw(50, 14, nil)
	out := strings.Join(lines, "\n")
	for _, want := range []string{"3 left", "Work", "☐ fix the tests", "find the flake", "Home", "buy milk", "call mom"} {
		if !strings.Contains(out, want) {
			t.Errorf("draw lacks %q:\n%s", want, out)
		}
	}
	for _, not := range []string{"Some notes", "Empty", " Todo"} {
		if strings.Contains(out, not) {
			t.Errorf("draw shows %q:\n%s", not, out)
		}
	}

	// Tick "ship it": only its box changes; the link and mode survive.
	td.Click(ctx, tokenFor(t, lines, "ship it"))
	b, _ := os.ReadFile(real)
	if want := strings.Replace(body, "- [ ] ship it", "- [x] ship it", 1); string(b) != want {
		t.Errorf("after ticking:\n%s", b)
	}
	if fi, _ := os.Lstat(link); fi.Mode()&os.ModeSymlink == 0 {
		t.Error("symlink replaced by a file")
	}
	if fi, _ := os.Stat(real); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v", fi.Mode().Perm())
	}
	if out := strings.Join(td.Draw(50, 14, nil), "\n"); !strings.Contains(out, "2 left") {
		t.Errorf("count not updated:\n%s", out)
	}
	// Untick a done one.
	td.Click(ctx, tokenFor(t, td.Draw(50, 14, nil), "buy milk"))
	if b, _ := os.ReadFile(real); !strings.Contains(string(b), "* [ ] buy milk") {
		t.Errorf("unticking:\n%s", b)
	}

	// A stale click (the file changed since it was drawn) does nothing.
	stale := tokenFor(t, td.Draw(50, 14, nil), "fix the tests")
	cur, _ := os.ReadFile(real)
	os.WriteFile(real, []byte("- [ ] a new first line\n"+string(cur)), 0o600)
	td.Click(ctx, stale)
	if b, _ := os.ReadFile(real); strings.Contains(string(b), "[x] fix the tests") || strings.Contains(string(b), "[x] a new") {
		t.Errorf("stale click changed the file:\n%s", b)
	}
	td.Click(ctx, "toggle:999:00000000")
	td.Click(ctx, "rm -rf /")

	// done = "hide".
	td.hideDone = true
	if out := strings.Join(td.Draw(50, 14, nil), "\n"); strings.Contains(out, "find the flake") {
		t.Errorf("done items shown with done=hide:\n%s", out)
	}
	for _, sz := range [][2]int{{50, 14}, {20, 3}} {
		for _, l := range td.Draw(sz[0], sz[1], nil) {
			if width(l) > sz[0] {
				t.Errorf("%v: too wide: %q", sz, l)
			}
		}
	}
}

func TestTodoCreate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notes", "todo.md")
	td := &todo{path: path}
	td.Update(context.Background())
	lines := td.Draw(50, 8, nil)
	if !strings.Contains(strings.Join(lines, "\n"), "create it") {
		t.Fatalf("no create button:\n%s", strings.Join(lines, "\n"))
	}
	td.Click(context.Background(), tokenFor(t, lines, "create it"))
	if b, err := os.ReadFile(path); err != nil || !strings.Contains(string(b), "- [ ]") {
		t.Fatalf("created %q, %v", b, err)
	}
	if out := strings.Join(td.Draw(50, 8, nil), "\n"); !strings.Contains(out, "2 left") {
		t.Errorf("after create:\n%s", out)
	}
	// It doesn't overwrite a file that appeared meanwhile.
	os.WriteFile(path, []byte("- [ ] mine\n"), 0o644)
	td.Click(context.Background(), "create")
	if b, _ := os.ReadFile(path); string(b) != "- [ ] mine\n" {
		t.Errorf("create overwrote: %q", b)
	}
}

func TestTodoAdd(t *testing.T) {
	path := filepath.Join(t.TempDir(), "todo.md")
	body := "# Todo\n\n* [ ] first\n  * [ ] sub\n\nnotes at the end\n"
	os.WriteFile(path, []byte(body), 0o644)
	td := &todo{path: path}
	ctx := context.Background()
	td.Update(ctx)

	// "+ add" in the title starts it.
	td.Click(ctx, tokenFor(t, td.Draw(50, 10, nil), "+ add"))
	if !td.adding {
		t.Fatal("+ add didn't start adding")
	}
	for _, k := range []string{"b", "u", "y", " ", "e", "g", "x", "\x7f", "g", "s", "\x1b[D", "\r"} {
		td.Type(ctx, []byte(k))
	}
	b, _ := os.ReadFile(path)
	if want := "# Todo\n\n* [ ] first\n  * [ ] sub\n* [ ] buy eggs\n\nnotes at the end\n"; string(b) != want {
		t.Errorf("after adding:\n%q", b)
	}
	if !td.adding || len(td.draft) != 0 {
		t.Error("should stay adding with an empty draft")
	}
	if out := strings.Join(td.Draw(50, 10, nil), "\n"); !strings.Contains(out, "buy eggs") || !strings.Contains(out, "esc stops") {
		t.Errorf("draw:\n%s", out)
	}

	// A paste of two lines adds both; Ctrl+W and Ctrl+U edit.
	td.Type(ctx, []byte("one\ntwo\r"))
	td.Type(ctx, []byte("three four"))
	td.Type(ctx, []byte{0x17})
	td.Type(ctx, []byte("five"))
	td.Type(ctx, []byte("\r"))
	td.Type(ctx, []byte("junk"))
	td.Type(ctx, []byte{0x15})
	td.Type(ctx, []byte("\r")) // empty: done
	if td.adding {
		t.Error("Enter on an empty line should stop")
	}
	b, _ = os.ReadFile(path)
	if !strings.Contains(string(b), "* [ ] buy eggs\n* [ ] one\n* [ ] two\n* [ ] three five\n\nnotes") || strings.Contains(string(b), "junk") {
		t.Errorf("after paste:\n%s", b)
	}

	// Typing starts adding; Esc drops the draft.
	td.Type(ctx, []byte("x"))
	td.Type(ctx, []byte("\x1b"))
	if td.adding || len(td.draft) != 0 {
		t.Error("Esc should stop and drop the draft")
	}
	// Arrows and Backspace alone don't start it.
	td.Type(ctx, []byte("\x1b[A"))
	td.Type(ctx, []byte("\x7f"))
	if td.adding {
		t.Error("non-text keys started adding")
	}
}

func TestTodoAddNewFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notes", "todo.md")
	td := &todo{path: path}
	ctx := context.Background()
	td.Update(ctx)
	td.Type(ctx, []byte("water plants\r"))
	b, err := os.ReadFile(path)
	if err != nil || string(b) != "# Todo\n\n- [ ] water plants\n" {
		t.Errorf("new file: %q %v", b, err)
	}
	// After plain text, the item gets a blank line before it.
	os.WriteFile(path, []byte("just notes\n"), 0o644)
	td.Type(ctx, []byte("a\r"))
	if b, _ := os.ReadFile(path); string(b) != "just notes\n\n- [ ] a\n" {
		t.Errorf("after notes: %q", b)
	}
}

func TestTodoAddScrolls(t *testing.T) {
	path := filepath.Join(t.TempDir(), "todo.md")
	var sb strings.Builder
	for i := range 20 {
		fmt.Fprintf(&sb, "- [ ] item %d\n", i)
	}
	os.WriteFile(path, []byte(sb.String()), 0o644)
	td := &todo{path: path}
	ctx := context.Background()
	td.Update(ctx)
	td.Type(ctx, []byte("new"))
	lines := td.Draw(40, 8, nil)
	out := strings.Join(lines, "\n")
	if len(lines) > 8 || !strings.Contains(out, "item 19") || !strings.Contains(out, "new") || strings.Contains(out, "item 0\x1b") {
		t.Errorf("while adding, the end of the list and the input show:\n%s", out)
	}
}

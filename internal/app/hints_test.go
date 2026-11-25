package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/zach-monroe/germ/internal/term"
)

func TestFindHints(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "internal/app"), 0o755)
	os.WriteFile(filepath.Join(dir, "internal/app/search.go"), nil, 0o644)

	tm := term.New(60, 8, term.Options{Scrollback: 100})
	tm.Write([]byte("old line off screen https://old.example\r\n"))
	for i := 0; i < 8; i++ {
		tm.Write([]byte("\r\n"))
	}
	tm.Write([]byte("see https://example.com/docs. and internal/app/search.go:42\r\n"))
	tm.Write([]byte("commit 3f9a2c1 on 10.0.0.12:8080, not deadbeef or 1234567\r\n"))
	tm.Write([]byte("missing/file.go:3 is not a hint; vite on localhost:5173\r\n"))
	tm.Write([]byte("\x1b]8;;https://linked.example\x1b\\click me\x1b]8;;\x1b\\\r\n"))

	hs := findHints(tm, 0, dir)
	want := []struct {
		kind hintKind
		text string
	}{
		{hintURL, "https://example.com/docs"},
		{hintFile, "internal/app/search.go:42"},
		{hintHash, "3f9a2c1"},
		{hintIP, "10.0.0.12:8080"},
		{hintIP, "localhost:5173"},
		{hintURL, "https://linked.example"},
	}
	if len(hs) != len(want) {
		for _, h := range hs {
			t.Logf("%d %q", h.kind, h.text)
		}
		t.Fatalf("got %d hints, want %d", len(hs), len(want))
	}
	for i, w := range want {
		if hs[i].kind != w.kind || hs[i].text != w.text {
			t.Errorf("hint %d: got %d %q, want %d %q", i, hs[i].kind, hs[i].text, w.kind, w.text)
		}
	}
	if hs[1].file == nil || hs[1].file.line != 42 {
		t.Errorf("file ref: %+v", hs[1].file)
	}

	labelHints(hs)
	if hs[len(hs)-1].label != "a" || hs[0].label != "k" {
		t.Errorf("labels: newest %q oldest %q", hs[len(hs)-1].label, hs[0].label)
	}
	many := make([]hint, 30)
	labelHints(many)
	seen := map[string]bool{}
	for _, h := range many {
		if len(h.label) != 2 || seen[h.label] {
			t.Fatalf("bad two-letter label %q", h.label)
		}
		seen[h.label] = true
	}
}

package app

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/zach-monroe/germ/internal/term"
)

func TestFindFile(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "internal/app"), 0o755)
	os.WriteFile(filepath.Join(dir, "internal/app/x.go"), nil, 0o644)
	os.WriteFile(filepath.Join(dir, "README.md"), nil, 0o644)
	tm := term.New(60, 3, term.Options{})
	tm.Write([]byte("error at internal/app/x.go:45:3: oops\r\nsee README.md. and missing.go:3"))
	tm.Lock()
	defer tm.Unlock()
	ref, link := findFile(tm, 0, 12, dir)
	if ref == nil || ref.path != filepath.Join(dir, "internal/app/x.go") || ref.line != 45 || ref.col != 3 {
		t.Fatalf("ref %+v", ref)
	}
	if link.StartCol != 9 || link.EndCol != 30 {
		t.Errorf("link %+v", link)
	}
	if ref, _ := findFile(tm, 1, 6, dir); ref == nil || ref.path != filepath.Join(dir, "README.md") || ref.line != 0 {
		t.Errorf("README: %+v", ref)
	}
	if ref, _ := findFile(tm, 1, 22, dir); ref != nil {
		t.Errorf("missing file matched: %+v", ref)
	}
	if ref, _ := findFile(tm, 0, 2, dir); ref != nil {
		t.Errorf("plain word matched: %+v", ref)
	}
}

func TestEditorCommand(t *testing.T) {
	ref := &fileRef{path: "/r/a.go", line: 12, col: 4}
	for _, c := range []struct {
		editor []string
		want   []string
		gui    bool
	}{
		{[]string{"nvim"}, []string{"nvim", "+12", "/r/a.go"}, false},
		{[]string{"hx"}, []string{"hx", "/r/a.go:12:4"}, false},
		{[]string{"code", "--reuse-window"}, []string{"code", "--reuse-window", "-g", "/r/a.go:12:4"}, true},
	} {
		got, gui := editorCommand(c.editor, ref)
		if !reflect.DeepEqual(got, c.want) || gui != c.gui {
			t.Errorf("%v: got %v %v", c.editor, got, gui)
		}
	}
}

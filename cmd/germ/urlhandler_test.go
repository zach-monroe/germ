package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTemporaryBuild(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	cases := map[string]bool{
		filepath.Join(tmp, "go-build123", "b001", "exe", "germ"): true,
		filepath.Join(tmp, "scratchpad", "germ"):                 true,
		"/usr/local/bin/germ":                                    false,
		tmp + "-other/germ":                                      false,
	}
	for p, want := range cases {
		if got := temporaryBuild(p); got != want {
			t.Errorf("%s: %v, want %v", p, got, want)
		}
	}
}

// Hosting or joining from a temporary build leaves the germ:// handler
// alone; germ setup registers it from anywhere.
func TestEnsureURLHandlerSkipsTemporaryBuilds(t *testing.T) {
	data := t.TempDir()
	t.Setenv("XDG_DATA_HOME", data)
	t.Setenv("PATH", t.TempDir()) // no xdg-mime or update-desktop-database
	self, _ := os.Executable()
	t.Setenv("TMPDIR", filepath.Dir(self))
	entry := filepath.Join(data, "applications", "germ-join.desktop")
	if err := ensureURLHandler(true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(entry); err == nil {
		t.Fatal("temporary build registered itself for germ:// links")
	}
	if err := ensureURLHandler(false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(entry); err != nil {
		t.Fatal("germ setup did not register:", err)
	}
}

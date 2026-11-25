package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetTheme(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	for _, c := range []struct{ in, want, old string }{
		{"", "theme = \"nord\"\n", ""},
		{"# my config\n\n[font]\nsize = 12\n", "# my config\n\ntheme = \"nord\"\n[font]\nsize = 12\n", ""},
		{"# hi\ntheme = \"dracula\"  # dark\n[font]\nsize = 12\n", "# hi\ntheme = \"nord\"  # dark\n[font]\nsize = 12\n", "dracula"},
		{"font_x = 1\n[colors]\ntheme = \"x\"\n", "theme = \"nord\"\nfont_x = 1\n[colors]\ntheme = \"x\"\n", ""},
	} {
		os.WriteFile(path, []byte(c.in), 0o644)
		if c.in == "" {
			os.Remove(path)
		}
		old, err := SetTheme(path, "nord")
		got, _ := os.ReadFile(path)
		if err != nil || string(got) != c.want || old != c.old {
			t.Errorf("SetTheme on %q:\ngot  %q (old %q, %v)\nwant %q (old %q)", c.in, got, old, err, c.want, c.old)
		}
	}
	// A symlinked config stays a symlink.
	real := filepath.Join(dir, "real.toml")
	os.WriteFile(real, []byte("theme = \"a\"\n"), 0o644)
	link := filepath.Join(dir, "link.toml")
	os.Symlink(real, link)
	if _, err := SetTheme(link, "b"); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Lstat(link); fi.Mode()&os.ModeSymlink == 0 {
		t.Error("symlink replaced")
	}
	if b, _ := os.ReadFile(real); !strings.Contains(string(b), `"b"`) {
		t.Errorf("real file: %s", b)
	}
}

package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDefaultsAndMissingFile(t *testing.T) {
	cfg, warn, err := Load(filepath.Join(t.TempDir(), "nope.toml"))
	if err != nil || len(warn) != 0 {
		t.Fatal(err, warn)
	}
	if cfg.Font.Family != "monospace" || cfg.Window.Columns != 100 || cfg.Theme != "tokyonight" {
		t.Errorf("defaults: %+v", cfg)
	}
}

func TestParse(t *testing.T) {
	cfg, warn, err := Parse([]byte(`
theme = "nord"
bogus = 1
[font]
family = "JetBrainsMono Nerd Font"
size = 10
[colors]
background = "#000000"
[keybindings]
"ctrl+shift+c" = "none"
"alt+c" = "copy"
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(warn) != 1 {
		t.Errorf("warnings: %v", warn)
	}
	if cfg.Font.Size != 10 || cfg.Window.Rows != 30 {
		t.Errorf("merge with defaults: %+v", cfg)
	}
	th, err := cfg.ResolveTheme()
	if err != nil {
		t.Fatal(err)
	}
	if th.Background.Hex() != "#000000" || th.Foreground.Hex() != "#d8dee9" {
		t.Errorf("override: bg=%s fg=%s", th.Background.Hex(), th.Foreground.Hex())
	}
	b, _ := cfg.Bindings()
	if _, ok := b[Chord{ModCtrl | ModShift, "c"}]; ok {
		t.Error("ctrl+shift+c should be unbound")
	}
	if b[Chord{ModAlt, "c"}] != ActionCopy {
		t.Error("alt+c should copy")
	}
}

func TestBadConfig(t *testing.T) {
	for _, s := range []string{
		`[keybindings]
"ctrl+shift+c" = "explode"`,
		`[keybindings]
"ctrl+nope+x" = "copy"`,
		`font = 3`,
	} {
		if _, _, err := Parse([]byte(s)); err == nil {
			t.Errorf("expected error for %q", s)
		}
	}
}

func TestParseChord(t *testing.T) {
	cases := map[string]Chord{
		"ctrl+shift+c":    {ModCtrl | ModShift, "c"},
		"Ctrl+Shift+PgUp": {ModCtrl | ModShift, "pageup"},
		"ctrl++":          {ModCtrl, "="},
		"super+f5":        {ModSuper, "f5"},
		"shift+insert":    {ModShift, "insert"},
	}
	for in, want := range cases {
		got, err := ParseChord(in)
		if err != nil || got != want {
			t.Errorf("%q: %+v %v", in, got, err)
		}
	}
}

func TestWatch(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	os.MkdirAll(Dir(), 0o755)
	changed := make(chan struct{}, 4)
	stop, err := Watch("", func() { changed <- struct{}{} })
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	// Simulate an editor's atomic save: write a temp file, rename it over.
	tmp := filepath.Join(Dir(), ".config.toml.tmp")
	os.WriteFile(tmp, []byte(`theme = "dracula"`), 0o644)
	os.Rename(tmp, Path())
	select {
	case <-changed:
	case <-time.After(2 * time.Second):
		t.Fatal("no change notification")
	}
}

// A config given with -config elsewhere, and one linked from a dotfiles
// repo, reload when saved.
func TestWatchPath(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, link := range []bool{false, true} {
		dir := t.TempDir()
		real := filepath.Join(dir, "germ.toml")
		os.WriteFile(real, nil, 0o644)
		path := real
		if link {
			path = filepath.Join(t.TempDir(), "config.toml")
			os.Symlink(real, path)
		}
		changed := make(chan struct{}, 4)
		stop, err := Watch(path, func() { changed <- struct{}{} })
		if err != nil {
			t.Fatal(err)
		}
		tmp := real + ".tmp"
		os.WriteFile(tmp, []byte(`theme = "dracula"`), 0o644)
		os.Rename(tmp, real)
		select {
		case <-changed:
		case <-time.After(2 * time.Second):
			t.Errorf("link=%v: no change notification", link)
		}
		stop()
	}
}

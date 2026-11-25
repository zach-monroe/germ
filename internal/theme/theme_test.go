package theme

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBundledThemesParse(t *testing.T) {
	names := Bundled()
	if len(names) < 5 {
		t.Fatalf("only %d bundled themes", len(names))
	}
	for _, n := range names {
		th, err := Load(n)
		if err != nil {
			t.Errorf("%s: %v", n, err)
			continue
		}
		if th.Name == "" || th.Foreground == th.Background {
			t.Errorf("%s: suspicious theme %+v", n, th)
		}
		if th.Palette[16] != (RGB{0, 0, 0}) || th.Palette[231] != (RGB{255, 255, 255}) {
			t.Errorf("%s: extended palette not filled", n)
		}
	}
}

func TestParseColor(t *testing.T) {
	cases := map[string]RGB{
		"#abc":               {0xaa, 0xbb, 0xcc},
		"#1a1b26":            {0x1a, 0x1b, 0x26},
		"#ffff00000000":      {0xff, 0, 0},
		"rgb:ff/80/00":       {0xff, 0x80, 0},
		"rgb:ffff/0000/8080": {0xff, 0, 0x80},
		"rgb:f/0/8":          {0xff, 0, 0x88},
	}
	for in, want := range cases {
		got, err := ParseColor(in)
		if err != nil || got != want {
			t.Errorf("%q: got %v %v, want %v", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "red", "#12", "#gggggg", "rgb:1/2"} {
		if _, err := ParseColor(bad); err == nil {
			t.Errorf("%q: expected error", bad)
		}
	}
}

func TestLoadUserThemeAndErrors(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "mine.toml"), []byte(`
name = "Mine"
foreground = "#ffffff"
background = "#000000"
normal = ["#000000","#110000","#001100","#111100","#000011","#110011","#001111","#cccccc"]
bright = ["#333333","#ff0000","#00ff00","#ffff00","#0000ff","#ff00ff","#00ffff","#ffffff"]
`), 0o644)
	th, err := Load("mine", dir)
	if err != nil {
		t.Fatal(err)
	}
	if th.Palette[1] != (RGB{0x11, 0, 0}) || th.Cursor != th.Foreground {
		t.Errorf("user theme: %+v", th)
	}
	if _, err := Load("nope"); err == nil {
		t.Error("expected unknown theme error")
	}
	os.WriteFile(filepath.Join(dir, "bad.toml"), []byte("foreground = \"#fff\"\nbackground = \"#000\"\nnormal = [\"#000\"]\n"), 0o644)
	if _, err := Load("bad", dir); err == nil {
		t.Error("expected error for short palette")
	}
	os.WriteFile(filepath.Join(dir, "typo.toml"), []byte("foregroud = \"#fff\"\n"), 0o644)
	if _, err := Load("typo", dir); err == nil {
		t.Error("expected unknown-key error")
	}
}

func TestTemplateRoundTrip(t *testing.T) {
	for _, name := range Bundled() {
		orig, err := Load(name)
		if err != nil {
			t.Fatal(err)
		}
		back, err := Parse(Template("x", orig))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		back.Name = orig.Name
		if *back != *orig {
			t.Errorf("%s: template changed the colors", name)
		}
	}
}

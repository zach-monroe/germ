// Package theme defines color schemes: the Theme type, parsing theme files,
// and the bundled themes.
package theme

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/BurntSushi/toml"
)

// RGB is a 24-bit color.
type RGB struct{ R, G, B uint8 }

// Hex formats c as #rrggbb.
func (c RGB) Hex() string { return fmt.Sprintf("#%02x%02x%02x", c.R, c.G, c.B) }

// Theme is a complete terminal color scheme.
type Theme struct {
	Name        string
	Foreground  RGB
	Background  RGB
	Cursor      RGB
	CursorText  RGB
	SelectionFG RGB
	SelectionBG RGB
	// Palette is the 256-color palette. Entries 0–15 come from the theme;
	// 16–255 are the standard xterm color cube and grayscale ramp.
	Palette [256]RGB
}

// DefaultName is the theme used when none is configured.
const DefaultName = "tokyonight"

//go:embed themes/*.toml
var bundled embed.FS

var defaultTheme = sync.OnceValue(func() Theme {
	t, err := Load(DefaultName)
	if err != nil {
		panic("theme: bundled default is broken: " + err.Error())
	}
	return *t
})

// Default returns (a copy of) the built-in default theme.
func Default() *Theme {
	t := defaultTheme()
	return &t
}

// File is the on-disk theme format.
type File struct {
	Name                string   `toml:"name"`
	Foreground          string   `toml:"foreground"`
	Background          string   `toml:"background"`
	Cursor              string   `toml:"cursor"`
	CursorText          string   `toml:"cursor_text"`
	SelectionForeground string   `toml:"selection_foreground"`
	SelectionBackground string   `toml:"selection_background"`
	Normal              []string `toml:"normal"`
	Bright              []string `toml:"bright"`
}

// Parse reads a theme file.
func Parse(data []byte) (*Theme, error) {
	var f File
	md, err := toml.Decode(string(data), &f)
	if err != nil {
		return nil, err
	}
	if u := md.Undecoded(); len(u) > 0 {
		return nil, fmt.Errorf("unknown key %q", u[0].String())
	}
	t := &Theme{Name: f.Name}
	FillExtended(&t.Palette)
	if err := f.Apply(t, true); err != nil {
		return nil, err
	}
	return t, nil
}

// Apply sets the colors present in f on t. If required is true, the
// foreground, background, and all 16 palette colors must be present.
func (f *File) Apply(t *Theme, required bool) error {
	set := func(name, v string, dst *RGB) error {
		if v == "" {
			if required && (name == "foreground" || name == "background") {
				return fmt.Errorf("missing %s", name)
			}
			return nil
		}
		c, err := ParseColor(v)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		*dst = c
		return nil
	}
	if err := set("foreground", f.Foreground, &t.Foreground); err != nil {
		return err
	}
	if err := set("background", f.Background, &t.Background); err != nil {
		return err
	}
	if required {
		// Sensible defaults for optional colors.
		t.Cursor, t.CursorText = t.Foreground, t.Background
		t.SelectionFG, t.SelectionBG = t.Background, t.Foreground
	}
	for _, s := range []struct {
		name, v string
		dst     *RGB
	}{
		{"cursor", f.Cursor, &t.Cursor},
		{"cursor_text", f.CursorText, &t.CursorText},
		{"selection_foreground", f.SelectionForeground, &t.SelectionFG},
		{"selection_background", f.SelectionBackground, &t.SelectionBG},
	} {
		if err := set(s.name, s.v, s.dst); err != nil {
			return err
		}
	}
	for i, group := range [][]string{f.Normal, f.Bright} {
		name := [2]string{"normal", "bright"}[i]
		if len(group) == 0 && !required {
			continue
		}
		if len(group) != 8 {
			return fmt.Errorf("%s: need 8 colors, got %d", name, len(group))
		}
		for j, v := range group {
			if err := set(fmt.Sprintf("%s[%d]", name, j), v, &t.Palette[i*8+j]); err != nil {
				return err
			}
		}
	}
	return nil
}

// Load finds a theme by name or path. A value containing '/' or ending in
// ".toml" is a file path; otherwise each of dirs is searched for
// "<name>.toml", then the bundled themes.
func Load(name string, dirs ...string) (*Theme, error) {
	if strings.ContainsRune(name, '/') || strings.HasSuffix(name, ".toml") {
		data, err := os.ReadFile(expandHome(name))
		if err != nil {
			return nil, err
		}
		t, err := Parse(data)
		if err != nil {
			return nil, fmt.Errorf("theme %s: %w", name, err)
		}
		return t, nil
	}
	for _, d := range dirs {
		if data, err := os.ReadFile(filepath.Join(d, name+".toml")); err == nil {
			t, err := Parse(data)
			if err != nil {
				return nil, fmt.Errorf("theme %s: %w", name, err)
			}
			return t, nil
		}
	}
	data, err := bundled.ReadFile("themes/" + name + ".toml")
	if err != nil {
		return nil, fmt.Errorf("unknown theme %q (bundled: %s)", name, strings.Join(Bundled(), ", "))
	}
	return Parse(data)
}

// Bundled lists the names of the built-in themes.
func Bundled() []string {
	entries, _ := bundled.ReadDir("themes")
	var names []string
	for _, e := range entries {
		names = append(names, strings.TrimSuffix(e.Name(), ".toml"))
	}
	sort.Strings(names)
	return names
}

func expandHome(p string) string {
	if strings.HasPrefix(p, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, p[2:])
		}
	}
	return p
}

// ParseColor parses "#rgb", "#rrggbb", or X11 "rgb:r/g/b" (1–4 hex digits
// per component).
func ParseColor(s string) (RGB, error) {
	s = strings.TrimSpace(s)
	switch {
	case strings.HasPrefix(s, "#"):
		h := s[1:]
		switch len(h) {
		case 3:
			h = string([]byte{h[0], h[0], h[1], h[1], h[2], h[2]})
		case 6:
		case 9, 12: // #rrrgggbbb, #rrrrggggbbbb: take the high byte
			n := len(h) / 3
			h = h[0:2] + h[n:n+2] + h[2*n:2*n+2]
		default:
			return RGB{}, fmt.Errorf("bad color %q", s)
		}
		v, err := strconv.ParseUint(h, 16, 32)
		if err != nil {
			return RGB{}, fmt.Errorf("bad color %q", s)
		}
		return RGB{uint8(v >> 16), uint8(v >> 8), uint8(v)}, nil
	case strings.HasPrefix(s, "rgb:"):
		parts := strings.Split(s[4:], "/")
		if len(parts) != 3 {
			return RGB{}, fmt.Errorf("bad color %q", s)
		}
		var c [3]uint8
		for i, p := range parts {
			if len(p) < 1 || len(p) > 4 {
				return RGB{}, fmt.Errorf("bad color %q", s)
			}
			v, err := strconv.ParseUint(p, 16, 16)
			if err != nil {
				return RGB{}, fmt.Errorf("bad color %q", s)
			}
			// Scale n hex digits to 8 bits.
			maxv := uint64(1)<<(4*len(p)) - 1
			c[i] = uint8((v*255 + maxv/2) / maxv)
		}
		return RGB{c[0], c[1], c[2]}, nil
	}
	return RGB{}, fmt.Errorf("bad color %q (use #rrggbb)", s)
}

// FillExtended sets palette entries 16–255 to the xterm defaults.
func FillExtended(p *[256]RGB) {
	levels := [6]uint8{0, 95, 135, 175, 215, 255}
	for i := 0; i < 216; i++ {
		p[16+i] = RGB{levels[i/36], levels[(i/6)%6], levels[i%6]}
	}
	for i := 0; i < 24; i++ {
		v := uint8(8 + 10*i)
		p[232+i] = RGB{v, v, v}
	}
}

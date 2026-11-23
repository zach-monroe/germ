// Package theme defines color schemes. Phase 4 adds loading themes from
// TOML; for now there is a built-in default.
package theme

// RGB is a 24-bit color.
type RGB struct{ R, G, B uint8 }

// Theme is a complete terminal color scheme.
type Theme struct {
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

// Default returns the built-in theme (Tokyo Night).
func Default() *Theme {
	t := &Theme{
		Foreground:  hex(0xc0caf5),
		Background:  hex(0x1a1b26),
		Cursor:      hex(0xc0caf5),
		CursorText:  hex(0x1a1b26),
		SelectionFG: hex(0xc0caf5),
		SelectionBG: hex(0x33467c),
	}
	base := [16]uint32{
		0x15161e, 0xf7768e, 0x9ece6a, 0xe0af68, 0x7aa2f7, 0xbb9af7, 0x7dcfff, 0xa9b1d6,
		0x414868, 0xf7768e, 0x9ece6a, 0xe0af68, 0x7aa2f7, 0xbb9af7, 0x7dcfff, 0xc0caf5,
	}
	for i, c := range base {
		t.Palette[i] = hex(c)
	}
	FillExtended(&t.Palette)
	return t
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

func hex(v uint32) RGB { return RGB{uint8(v >> 16), uint8(v >> 8), uint8(v)} }

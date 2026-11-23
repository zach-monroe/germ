// Package term holds terminal state: the character grid, cursor, scrollback,
// and modes. It interprets parsed escape sequences from package vt and has
// no dependency on windowing or rendering.
package term

// Color is a packed cell color: the top byte is a tag, the low 24 bits the
// value. The zero Color means "default foreground/background".
type Color uint32

const (
	tagMask    = 0xff << 24
	tagPalette = 1 << 24
	tagRGB     = 2 << 24
)

// DefaultColor is the terminal's default foreground or background.
const DefaultColor Color = 0

// PaletteColor returns entry i of the 256-color palette.
func PaletteColor(i uint8) Color { return Color(tagPalette | uint32(i)) }

// RGBColor returns a 24-bit color.
func RGBColor(r, g, b uint8) Color {
	return Color(tagRGB | uint32(r)<<16 | uint32(g)<<8 | uint32(b))
}

// IsDefault reports whether c is the default color.
func (c Color) IsDefault() bool { return c == DefaultColor }

// Palette returns the palette index if c is a palette color.
func (c Color) Palette() (uint8, bool) {
	return uint8(c), c&tagMask == tagPalette
}

// RGB returns the components if c is a 24-bit color.
func (c Color) RGB() (r, g, b uint8, ok bool) {
	return uint8(c >> 16), uint8(c >> 8), uint8(c), c&tagMask == tagRGB
}

// Attr is a set of cell attribute flags.
type Attr uint16

const (
	AttrBold Attr = 1 << iota
	AttrFaint
	AttrItalic
	AttrBlink
	AttrInverse
	AttrHidden
	AttrStrike
	AttrOverline

	// AttrWide marks the left half of a double-width character.
	AttrWide
	// AttrWideSpacer marks the right half of a double-width character.
	AttrWideSpacer
	// attrWrapPad marks the blank left at the end of a line when a wide
	// character wrapped early. Reflow drops it.
	attrWrapPad
)

// Underline is an underline style (SGR 4 / 4:n / 21).
type Underline uint8

const (
	UnderlineNone Underline = iota
	UnderlineSingle
	UnderlineDouble
	UnderlineCurly
	UnderlineDotted
	UnderlineDashed
)

// Cell is one grid position. It contains no pointers so the GC never has
// to scan the grid.
type Cell struct {
	Rune      rune // 0 means empty (rendered as a space)
	FG, BG    Color
	UL        Color // underline color; default means "same as FG"
	Attrs     Attr
	Underline Underline
}

// Line is one row of cells.
type Line struct {
	Cells []Cell
	// Wrapped is true if the text continues on the next line because it
	// hit the right margin (a soft wrap), rather than ending with a newline.
	Wrapped bool
	// Prompt marks a line where a shell prompt starts (OSC 133;A).
	Prompt bool
	// HasExit and Exit record the exit status of the command run from this
	// prompt line (OSC 133;D).
	HasExit bool
	Exit    int32
	// n is the written extent: every cell at index >= n is the zero Cell.
	// It lets clearing and rendering skip the empty tail of a line.
	n int
}

// NewLine returns a line holding cells (for UI chrome drawn like terminal
// text, e.g. a tab bar).
func NewLine(cells []Cell) *Line { return &Line{Cells: cells, n: len(cells)} }

// Extent returns an index past which all cells are empty zero Cells.
func (l *Line) Extent() int { return min(l.n, len(l.Cells)) }

func (l *Line) touch(end int) {
	if end > l.n {
		l.n = end
	}
}

// blank reports whether the line has no visible content.
func (l *Line) blank() bool {
	for i := range l.Cells {
		if l.Cells[i].Rune != 0 {
			return false
		}
	}
	return true
}

// textLen returns the length of the line ignoring trailing empty cells.
func (l *Line) textLen() int {
	n := len(l.Cells)
	for n > 0 && l.Cells[n-1].Rune == 0 && l.Cells[n-1].BG.IsDefault() &&
		l.Cells[n-1].Attrs&AttrWideSpacer == 0 {
		n--
	}
	return n
}

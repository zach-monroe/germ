package render

import (
	"time"

	"github.com/zach-monroe/germ/internal/term"
	"github.com/zach-monroe/germ/internal/theme"
)

// Selection is a range of absolute positions (see term.AbsRow), inclusive
// and normalized so Start comes first in reading order.
type Selection struct {
	Active           bool
	StartRow, EndRow int64
	StartCol, EndCol int
}

func (s *Selection) contains(row int64, col int) bool {
	if !s.Active || row < s.StartRow || row > s.EndRow {
		return false
	}
	if row == s.StartRow && col < s.StartCol {
		return false
	}
	if row == s.EndRow && col > s.EndCol {
		return false
	}
	return true
}

// Link is a hovered hyperlink span to underline.
type Link struct {
	Active           bool
	Row              int64
	StartCol, EndCol int
}

// View is how a terminal should be shown.
type View struct {
	Offset    int // lines scrolled back into history (0 = live)
	Focused   bool
	Selection Selection
	Link      Link
	Force     bool // rebuild even if nothing changed
}

// Surface is one terminal's place in the window and its cached instance
// data. Create one per pane with NewSurface.
type Surface struct {
	r         *Renderer
	rows      []rowCache
	cols      int
	cursor    cursorState
	reverse   bool
	view      View
	pushed    int64
	theme     theme.Theme // effective colors, copied when the terminal's change
	colorGen  uint64
	syncSince time.Time

	ox, oy   int // pixel origin of cell (0,0)
	gutter   int // pixels left of the origin available for marks
	fontGen  int
	atlasGen int
}

// NewSurface creates a surface.
func (r *Renderer) NewSurface() *Surface {
	return &Surface{r: r, colorGen: ^uint64(0), fontGen: -1}
}

// Update refreshes the surface from t, drawn with its top-left cell at
// pixel (ox, oy). gutter is how many pixels to the left of ox may hold
// marks. It returns false if nothing changed (and v.Force was not set).
func (s *Surface) Update(t *term.Terminal, v View, ox, oy, gutter int) bool {
	r := s.r
	force, focused := v.Force, v.Focused
	t.Lock()
	defer t.Unlock()
	modes := t.Modes()
	if modes.SyncOutput && !force {
		if s.syncSince.IsZero() {
			s.syncSince = time.Now()
		}
		if time.Since(s.syncSince) < syncTimeout {
			return false
		}
	} else {
		s.syncSince = time.Time{}
	}

	cols, rows := t.Size()
	dirty, all := t.TakeDirty()
	v.Offset = min(v.Offset, t.ScrollbackLen())
	pushed := t.Pushed()
	viewChanged := v.Offset != s.view.Offset || v.Selection != s.view.Selection || v.Link != s.view.Link
	moved := ox != s.ox || oy != s.oy || gutter != s.gutter
	if force || viewChanged || moved || rows != len(s.rows) || cols != s.cols || modes.ReverseVideo != s.reverse ||
		s.fontGen != r.fontGen || s.atlasGen != r.atlas.gen {
		all = true
	}
	if v.Offset != 0 && (pushed != s.pushed || anyTrue(dirty)) {
		// Dirty flags are in screen coordinates; when scrolled back the
		// mapping to viewport rows shifts, so just redraw everything.
		all = true
	}
	if th, gen := t.Colors(); gen != s.colorGen {
		s.theme, s.colorGen = *th, gen
		all = true
	}
	v.Force = false
	s.view, s.pushed = v, pushed
	s.ox, s.oy, s.gutter = ox, oy, gutter
	s.fontGen = r.fontGen
	if rows != len(s.rows) {
		s.rows = make([]rowCache, rows)
	}
	s.cols = cols
	s.reverse = modes.ReverseVideo

	c := t.Cursor()
	cy := c.Y + v.Offset // the cursor moves down as the view scrolls back
	cur := cursorState{x: c.X, y: cy, visible: modes.CursorVisible && cy < rows, style: t.CursorStyle(), focused: focused}
	changed := all
	if cur != s.cursor {
		// Rebuild the rows the cursor left and entered.
		if s.cursor.y < rows {
			dirty[s.cursor.y] = true
		}
		if cur.y < rows {
			dirty[cur.y] = true
		}
		s.cursor = cur
		changed = true
	}
	for {
		gen := r.atlas.gen
		for y := 0; y < rows; y++ {
			if all || dirty[y] {
				abs := t.AbsRow(y, v.Offset)
				s.buildRow(y, abs, t.LineAt(abs))
				dirty[y] = false
				changed = true
			}
		}
		if r.atlas.gen == gen {
			break
		}
		all = true // atlas was wiped mid-build: rebuild everything
	}
	s.atlasGen = r.atlas.gen
	return changed
}

// UpdateLine draws a single synthetic line (e.g. the tab bar) at pixel
// (ox, oy) with the given colors. It always rebuilds; it's one row.
func (s *Surface) UpdateLine(line *term.Line, th *theme.Theme, ox, oy int) {
	s.theme = *th
	s.ox, s.oy, s.gutter = ox, oy, 0
	s.cols = len(line.Cells)
	s.view = View{}
	s.cursor = cursorState{}
	s.reverse = false
	if len(s.rows) != 1 {
		s.rows = make([]rowCache, 1)
	}
	s.buildRow(0, -1, line)
	s.atlasGen, s.fontGen = s.r.atlas.gen, s.r.fontGen
}

func (s *Surface) defaultFG() theme.RGB {
	if s.reverse {
		return s.theme.Background
	}
	return s.theme.Foreground
}

func (s *Surface) defaultBG() theme.RGB {
	if s.reverse {
		return s.theme.Foreground
	}
	return s.theme.Background
}

// DefaultBG returns the surface's background color (for clearing).
func (s *Surface) DefaultBG() theme.RGB { return s.defaultBG() }

// Theme returns the surface's current colors.
func (s *Surface) Theme() *theme.Theme { return &s.theme }

func (s *Surface) resolve(c term.Color, def theme.RGB) theme.RGB {
	if i, ok := c.Palette(); ok {
		return s.theme.Palette[i]
	}
	if cr, cg, cb, ok := c.RGB(); ok {
		return theme.RGB{R: cr, G: cg, B: cb}
	}
	return def
}

// cellColors returns the foreground and background for a cell, and whether
// the background differs from the default.
func (s *Surface) cellColors(c *term.Cell) (fg, bg theme.RGB, paintBG bool) {
	fg = s.resolve(c.FG, s.defaultFG())
	bg = s.resolve(c.BG, s.defaultBG())
	paintBG = !c.BG.IsDefault() || s.reverse
	if c.Attrs&term.AttrInverse != 0 {
		fg, bg = bg, fg
		paintBG = true
	}
	if c.Attrs&term.AttrFaint != 0 {
		fg = blend(fg, bg, 0.5)
	}
	if c.Attrs&term.AttrHidden != 0 {
		fg = bg
	}
	return fg, bg, paintBG
}

var emptyLine term.Line

// buildRow regenerates the instance data for viewport row y, which shows
// absolute row abs (line may be nil for rows with no content).
func (s *Surface) buildRow(y int, abs int64, line *term.Line) {
	r := s.r
	rc := &s.rows[y]
	rc.bg, rc.deco, rc.glyphs = rc.bg[:0], rc.deco[:0], rc.glyphs[:0]
	if line == nil {
		line = &emptyLine
	}
	sel := &s.view.Selection
	selHere := sel.Active && abs >= sel.StartRow && abs <= sel.EndRow
	link := s.view.Link
	linkHere := link.Active && link.Row == abs
	cw, ch := r.m.CellW, r.m.CellH
	py := s.oy + y*ch

	cur := s.cursor
	cursorHere := cur.visible && cur.y == y
	block := cursorHere && cur.focused &&
		(cur.style == term.CursorDefault || cur.style == term.CursorBlinkBlock || cur.style == term.CursorSteadyBlock)

	// Cells past the line's extent are empty; only the cursor can need
	// drawing there.
	cells := line.Cells
	if len(cells) > s.cols {
		cells = cells[:s.cols]
	}
	end := min(line.Extent(), len(cells))
	if block {
		end = max(end, min(cur.x+1, len(cells)))
	}
	if selHere {
		end = len(cells) // selection paints empty cells too
	}
	for x := 0; x < end; x++ {
		c := &cells[x]
		if c.Attrs&term.AttrWideSpacer != 0 {
			continue
		}
		w := 1
		if c.Attrs&term.AttrWide != 0 {
			w = 2
		}
		px := s.ox + x*cw
		fg, bg, paintBG := s.cellColors(c)
		if selHere && sel.contains(abs, x) {
			fg, bg, paintBG = s.theme.SelectionFG, s.theme.SelectionBG, true
		}
		if block && (x == cur.x || (w == 2 && x+1 == cur.x)) {
			fg, bg, paintBG = s.theme.CursorText, s.theme.Cursor, true
		}

		if paintBG {
			col := toRGBA(bg)
			// Merge with the previous rect when contiguous and same color.
			if n := len(rc.bg); n > 0 && rc.bg[n-1].Color == col && int(rc.bg[n-1].X+rc.bg[n-1].W) == px {
				rc.bg[n-1].W += float32(w * cw)
			} else {
				rc.bg = append(rc.bg, rectInst{float32(px), float32(py), float32(w * cw), float32(ch), col})
			}
		}

		if c.Rune > ' ' && c.Attrs&term.AttrHidden == 0 {
			if e := r.atlas.get(c.Rune, styleOf(c.Attrs)); e.ok {
				rc.glyphs = append(rc.glyphs, glyphInst{
					X: float32(px + e.bounds.Min.X), Y: float32(py + e.bounds.Min.Y),
					W: float32(e.bounds.Dx()), H: float32(e.bounds.Dy()),
					U: float32(e.uv.Min.X), V: float32(e.uv.Min.Y),
					UW: float32(e.uv.Dx()), VH: float32(e.uv.Dy()),
					Color: toRGBA(fg),
				})
			}
			if c.Mark != 0 {
				if e := r.atlas.get(c.Mark, styleOf(c.Attrs)); e.ok {
					// Most fonts draw combining marks left of the pen (zero
					// advance, placed back over the previous character), so
					// put the pen after the base; others draw them inside
					// their own cell, over the base as is.
					mx := px
					if e.bounds.Max.X <= 0 {
						mx = px + w*cw
					}
					rc.glyphs = append(rc.glyphs, glyphInst{
						X: float32(mx + e.bounds.Min.X), Y: float32(py + e.bounds.Min.Y),
						W: float32(e.bounds.Dx()), H: float32(e.bounds.Dy()),
						U: float32(e.uv.Min.X), V: float32(e.uv.Min.Y),
						UW: float32(e.uv.Dx()), VH: float32(e.uv.Dy()),
						Color: toRGBA(fg),
					})
				}
			}
		}
		s.decorations(rc, c, px, py, w*cw, fg)
		if linkHere && x >= link.StartCol && x <= link.EndCol && c.Underline == term.UnderlineNone {
			rc.deco = append(rc.deco, rectInst{float32(px), float32(py + r.m.Underline), float32(w * cw), float32(r.m.Thickness), toRGBA(fg)})
		}
	}

	if cursorHere && !block {
		s.cursorShape(rc, cur, py)
	}
	// Failed commands get a mark in the left padding next to their prompt.
	if line.Prompt && line.HasExit && line.Exit != 0 && s.gutter >= 2 {
		w := max(2, s.gutter/2)
		rc.deco = append(rc.deco, rectInst{float32(s.ox - s.gutter), float32(py), float32(w), float32(ch), toRGBA(s.theme.Palette[1])})
	}
}

func (s *Surface) decorations(rc *rowCache, c *term.Cell, px, py, width int, fg theme.RGB) {
	if c.Underline == term.UnderlineNone && c.Attrs&(term.AttrStrike|term.AttrOverline) == 0 {
		return
	}
	r := s.r
	th := r.m.Thickness
	add := func(x, y, w, h int, col rgba) {
		rc.deco = append(rc.deco, rectInst{float32(x), float32(y), float32(w), float32(h), col})
	}
	col := toRGBA(fg)
	if c.Attrs&term.AttrStrike != 0 {
		add(px, py+r.m.CellH/2, width, th, col)
	}
	if c.Attrs&term.AttrOverline != 0 {
		add(px, py, width, th, col)
	}
	if c.Underline == term.UnderlineNone {
		return
	}
	ul := col
	if !c.UL.IsDefault() {
		ul = toRGBA(s.resolve(c.UL, fg))
	}
	uy := py + r.m.Underline
	switch c.Underline {
	case term.UnderlineSingle:
		add(px, uy, width, th, ul)
	case term.UnderlineDouble:
		add(px, uy-th, width, th, ul)
		add(px, min(uy+th, py+r.m.CellH-th), width, th, ul)
	case term.UnderlineCurly:
		// Approximate a wave with short alternating segments.
		seg := max(2, width/4)
		for x, up := 0, true; x < width; x, up = x+seg, !up {
			yy := uy
			if up {
				yy -= th
			}
			add(px+x, yy, min(seg, width-x), th, ul)
		}
	case term.UnderlineDotted:
		for x := 0; x < width; x += 2 * th {
			add(px+x, uy, th, th, ul)
		}
	case term.UnderlineDashed:
		dash := max(2, width/3)
		for x := 0; x < width; x += 2 * dash {
			add(px+x, uy, min(dash, width-x), th, ul)
		}
	}
}

func (s *Surface) cursorShape(rc *rowCache, cur cursorState, py int) {
	r := s.r
	cw, ch := r.m.CellW, r.m.CellH
	px := s.ox + cur.x*cw
	col := toRGBA(s.theme.Cursor)
	th := max(1, r.m.Thickness)
	add := func(x, y, w, h int) {
		rc.deco = append(rc.deco, rectInst{float32(x), float32(y), float32(w), float32(h), col})
	}
	switch {
	case !cur.focused: // hollow block
		add(px, py, cw, th)
		add(px, py+ch-th, cw, th)
		add(px, py, th, ch)
		add(px+cw-th, py, th, ch)
	case cur.style == term.CursorBlinkUnderline || cur.style == term.CursorSteadyUnderline:
		add(px, py+ch-2*th, cw, 2*th)
	case cur.style == term.CursorBlinkBar || cur.style == term.CursorSteadyBar:
		add(px, py, 2*th, ch)
	}
}

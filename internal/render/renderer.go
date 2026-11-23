// Package render draws a term.Terminal with OpenGL 3.3.
//
// Each frame is three instanced draw calls: background rectangles, glyph
// quads sampled from a glyph atlas, and decoration rectangles (underlines,
// strikethrough, cursor shapes). Instance data is cached per row and only
// rebuilt for rows the terminal reports as dirty.
package render

import (
	"time"
	"unsafe"

	"github.com/go-gl/gl/v3.3-core/gl"

	"github.com/zach-monroe/germ/internal/font"
	"github.com/zach-monroe/germ/internal/term"
	"github.com/zach-monroe/germ/internal/theme"
)

// syncTimeout bounds how long synchronized output (mode 2026) may hold
// back frames, in case an app never ends its update.
const syncTimeout = 200 * time.Millisecond

type rgba [4]uint8

type rectInst struct {
	X, Y, W, H float32
	Color      rgba
}

type glyphInst struct {
	X, Y, W, H   float32
	U, V, UW, VH float32
	Color        rgba
}

type rowCache struct {
	bg, deco []rectInst
	glyphs   []glyphInst
}

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

// View is how the terminal should be shown.
type View struct {
	Offset    int // lines scrolled back into history (0 = live)
	Focused   bool
	Selection Selection
	Link      Link
	Force     bool // redraw even if nothing changed
}

type cursorState struct {
	x, y    int
	visible bool
	style   term.CursorStyle
	focused bool
}

// Renderer draws terminals. It must be used from the GL thread.
type Renderer struct {
	fonts *font.Set
	m     font.Metrics
	theme theme.Theme // effective colors, copied from the terminal each frame they change
	atlas *atlas
	pad   int

	rectProg, glyphProg uint32
	bgBuf, decoBuf      instanceBuffer
	glyphBuf            instanceBuffer
	uRectViewport       int32
	uGlyphViewport      int32
	uAtlasSize          int32

	width, height int // framebuffer size
	rows          []rowCache
	cols          int
	cursor        cursorState
	reverse       bool
	view          View
	pushed        int64
	colorGen      uint64
	syncSince     time.Time

	bg, deco []rectInst
	glyphs   []glyphInst
}

type instanceBuffer struct {
	vao, vbo uint32
	cap      int // bytes allocated on the GPU
}

// New creates a renderer. pad is the inner window padding in pixels.
func New(fonts *font.Set, pad int) (*Renderer, error) {
	r := &Renderer{fonts: fonts, m: fonts.Metrics(), pad: pad, colorGen: ^uint64(0)}
	var err error
	if r.rectProg, err = compileProgram(rectVert, rectFrag); err != nil {
		return nil, err
	}
	if r.glyphProg, err = compileProgram(glyphVert, glyphFrag); err != nil {
		return nil, err
	}
	r.uRectViewport = uniform(r.rectProg, "uViewport")
	r.uGlyphViewport = uniform(r.glyphProg, "uViewport")
	r.uAtlasSize = uniform(r.glyphProg, "uAtlasSize")
	gl.UseProgram(r.glyphProg)
	gl.Uniform1i(uniform(r.glyphProg, "uAtlas"), 0)

	r.bgBuf = newRectBuffer()
	r.decoBuf = newRectBuffer()
	r.glyphBuf = newGlyphBuffer()
	r.atlas = newAtlas(fonts)

	gl.Enable(gl.BLEND)
	gl.BlendFunc(gl.SRC_ALPHA, gl.ONE_MINUS_SRC_ALPHA)
	return r, nil
}

func newRectBuffer() instanceBuffer {
	var b instanceBuffer
	gl.GenVertexArrays(1, &b.vao)
	gl.GenBuffers(1, &b.vbo)
	gl.BindVertexArray(b.vao)
	gl.BindBuffer(gl.ARRAY_BUFFER, b.vbo)
	stride := int32(unsafe.Sizeof(rectInst{}))
	gl.EnableVertexAttribArray(0)
	gl.VertexAttribPointerWithOffset(0, 4, gl.FLOAT, false, stride, 0)
	gl.VertexAttribDivisor(0, 1)
	gl.EnableVertexAttribArray(1)
	gl.VertexAttribPointerWithOffset(1, 4, gl.UNSIGNED_BYTE, true, stride, 16)
	gl.VertexAttribDivisor(1, 1)
	return b
}

func newGlyphBuffer() instanceBuffer {
	var b instanceBuffer
	gl.GenVertexArrays(1, &b.vao)
	gl.GenBuffers(1, &b.vbo)
	gl.BindVertexArray(b.vao)
	gl.BindBuffer(gl.ARRAY_BUFFER, b.vbo)
	stride := int32(unsafe.Sizeof(glyphInst{}))
	gl.EnableVertexAttribArray(0)
	gl.VertexAttribPointerWithOffset(0, 4, gl.FLOAT, false, stride, 0)
	gl.VertexAttribDivisor(0, 1)
	gl.EnableVertexAttribArray(1)
	gl.VertexAttribPointerWithOffset(1, 4, gl.FLOAT, false, stride, 16)
	gl.VertexAttribDivisor(1, 1)
	gl.EnableVertexAttribArray(2)
	gl.VertexAttribPointerWithOffset(2, 4, gl.UNSIGNED_BYTE, true, stride, 32)
	gl.VertexAttribDivisor(2, 1)
	return b
}

// SetFonts switches to a new font set (e.g. after a zoom). The caller
// should call Resize afterwards since the cell size changes.
func (r *Renderer) SetFonts(fonts *font.Set) {
	gl.DeleteTextures(1, &r.atlas.tex)
	r.fonts, r.m = fonts, fonts.Metrics()
	r.atlas = newAtlas(fonts)
	r.rows = nil // force a full rebuild
}

// CellSize returns the cell size in pixels.
func (r *Renderer) CellSize() (w, h int) { return r.m.CellW, r.m.CellH }

// SetPad changes the window padding. Call Resize afterwards.
func (r *Renderer) SetPad(pad int) {
	r.pad = max(0, pad)
	r.rows = nil
}

// Pad returns the window padding in pixels.
func (r *Renderer) Pad() int { return r.pad }

// Resize sets the framebuffer size and returns the grid that fits in it.
func (r *Renderer) Resize(width, height int) (cols, rows int) {
	r.width, r.height = width, height
	gl.Viewport(0, 0, int32(width), int32(height))
	return r.GridSize(width, height)
}

// GridSize returns the grid that fits in a framebuffer of the given size.
func (r *Renderer) GridSize(width, height int) (cols, rows int) {
	cols = max(1, (width-2*r.pad)/r.m.CellW)
	rows = max(1, (height-2*r.pad)/r.m.CellH)
	return cols, rows
}

// Draw renders t if anything changed (or v.Force is set). It returns
// false if nothing was drawn, in which case the caller should not swap.
func (r *Renderer) Draw(t *term.Terminal, v View) bool {
	force, focused := v.Force, v.Focused
	t.Lock()
	modes := t.Modes()
	if modes.SyncOutput && !force {
		if r.syncSince.IsZero() {
			r.syncSince = time.Now()
		}
		if time.Since(r.syncSince) < syncTimeout {
			t.Unlock()
			return false
		}
	} else {
		r.syncSince = time.Time{}
	}

	cols, rows := t.Size()
	dirty, all := t.TakeDirty()
	v.Offset = min(v.Offset, t.ScrollbackLen())
	pushed := t.Pushed()
	viewChanged := v.Offset != r.view.Offset || v.Selection != r.view.Selection || v.Link != r.view.Link
	if force || viewChanged || rows != len(r.rows) || cols != r.cols || modes.ReverseVideo != r.reverse {
		all = true
	}
	if v.Offset != 0 && (pushed != r.pushed || anyTrue(dirty)) {
		// Dirty flags are in screen coordinates; when scrolled back the
		// mapping to viewport rows shifts, so just redraw everything.
		all = true
	}
	if th, gen := t.Colors(); gen != r.colorGen {
		r.theme, r.colorGen = *th, gen
		all = true
	}
	v.Force = false
	r.view, r.pushed = v, pushed
	if rows != len(r.rows) {
		r.rows = make([]rowCache, rows)
	}
	r.cols = cols
	r.reverse = modes.ReverseVideo

	c := t.Cursor()
	cy := c.Y + v.Offset // the cursor moves down as the view scrolls back
	cur := cursorState{x: c.X, y: cy, visible: modes.CursorVisible && cy < rows, style: t.CursorStyle(), focused: focused}
	changed := all
	if cur != r.cursor {
		// Rebuild the rows the cursor left and entered.
		if r.cursor.y < rows {
			dirty[r.cursor.y] = true
		}
		if cur.y < rows {
			dirty[cur.y] = true
		}
		r.cursor = cur
	}
	for {
		gen := r.atlas.gen
		for y := 0; y < rows; y++ {
			if all || dirty[y] {
				abs := t.AbsRow(y, v.Offset)
				r.buildRow(y, abs, t.LineAt(abs))
				dirty[y] = false
				changed = true
			}
		}
		if r.atlas.gen == gen {
			break
		}
		all = true // atlas was wiped mid-frame: rebuild everything
	}
	t.Unlock()

	if !changed {
		return false
	}
	r.flush()
	return true
}

// flush concatenates the row caches and issues the draw calls.
func (r *Renderer) flush() {
	r.bg, r.deco, r.glyphs = r.bg[:0], r.deco[:0], r.glyphs[:0]
	for i := range r.rows {
		r.bg = append(r.bg, r.rows[i].bg...)
		r.deco = append(r.deco, r.rows[i].deco...)
		r.glyphs = append(r.glyphs, r.rows[i].glyphs...)
	}

	bg := r.defaultBG()
	gl.ClearColor(float32(bg.R)/255, float32(bg.G)/255, float32(bg.B)/255, 1)
	gl.Clear(gl.COLOR_BUFFER_BIT)
	vw, vh := float32(r.width), float32(r.height)

	gl.UseProgram(r.rectProg)
	gl.Uniform2f(r.uRectViewport, vw, vh)
	drawInstances(&r.bgBuf, r.bg)

	gl.UseProgram(r.glyphProg)
	gl.Uniform2f(r.uGlyphViewport, vw, vh)
	gl.Uniform2f(r.uAtlasSize, float32(r.atlas.size), float32(r.atlas.size))
	gl.ActiveTexture(gl.TEXTURE0)
	gl.BindTexture(gl.TEXTURE_2D, r.atlas.tex)
	drawInstances(&r.glyphBuf, r.glyphs)

	gl.UseProgram(r.rectProg)
	drawInstances(&r.decoBuf, r.deco)
}

func drawInstances[T any](b *instanceBuffer, data []T) {
	if len(data) == 0 {
		return
	}
	size := len(data) * int(unsafe.Sizeof(data[0]))
	gl.BindVertexArray(b.vao)
	gl.BindBuffer(gl.ARRAY_BUFFER, b.vbo)
	if size > b.cap {
		b.cap = size * 2
		gl.BufferData(gl.ARRAY_BUFFER, b.cap, nil, gl.STREAM_DRAW)
	}
	gl.BufferSubData(gl.ARRAY_BUFFER, 0, size, gl.Ptr(data))
	gl.DrawArraysInstanced(gl.TRIANGLE_STRIP, 0, 4, int32(len(data)))
}

func (r *Renderer) defaultFG() theme.RGB {
	if r.reverse {
		return r.theme.Background
	}
	return r.theme.Foreground
}

func (r *Renderer) defaultBG() theme.RGB {
	if r.reverse {
		return r.theme.Foreground
	}
	return r.theme.Background
}

func (r *Renderer) resolve(c term.Color, def theme.RGB) theme.RGB {
	if i, ok := c.Palette(); ok {
		return r.theme.Palette[i]
	}
	if cr, cg, cb, ok := c.RGB(); ok {
		return theme.RGB{R: cr, G: cg, B: cb}
	}
	return def
}

// cellColors returns the foreground and background for a cell, and whether
// the background differs from the window's clear color.
func (r *Renderer) cellColors(c *term.Cell) (fg, bg theme.RGB, paintBG bool) {
	fg = r.resolve(c.FG, r.defaultFG())
	bg = r.resolve(c.BG, r.defaultBG())
	paintBG = !c.BG.IsDefault()
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

func blend(a, b theme.RGB, t float64) theme.RGB {
	mix := func(x, y uint8) uint8 { return uint8(float64(x)*(1-t) + float64(y)*t) }
	return theme.RGB{R: mix(a.R, b.R), G: mix(a.G, b.G), B: mix(a.B, b.B)}
}

func toRGBA(c theme.RGB) rgba { return rgba{c.R, c.G, c.B, 255} }

func styleOf(a term.Attr) font.Style {
	var s font.Style
	if a&term.AttrBold != 0 {
		s |= font.Bold
	}
	if a&term.AttrItalic != 0 {
		s |= font.Italic
	}
	return s
}

func anyTrue(b []bool) bool {
	for _, v := range b {
		if v {
			return true
		}
	}
	return false
}

var emptyLine term.Line

// buildRow regenerates the instance data for viewport row y, which shows
// absolute row abs (line may be nil for rows with no content).
func (r *Renderer) buildRow(y int, abs int64, line *term.Line) {
	rc := &r.rows[y]
	rc.bg, rc.deco, rc.glyphs = rc.bg[:0], rc.deco[:0], rc.glyphs[:0]
	if line == nil {
		line = &emptyLine
	}
	sel := &r.view.Selection
	selHere := sel.Active && abs >= sel.StartRow && abs <= sel.EndRow
	link := r.view.Link
	linkHere := link.Active && link.Row == abs
	cw, ch := r.m.CellW, r.m.CellH
	py := r.pad + y*ch

	cur := r.cursor
	cursorHere := cur.visible && cur.y == y
	block := cursorHere && cur.focused &&
		(cur.style == term.CursorDefault || cur.style == term.CursorBlinkBlock || cur.style == term.CursorSteadyBlock)

	// Cells past the line's extent are empty; only the cursor can need
	// drawing there.
	cells := line.Cells
	if len(cells) > r.cols {
		cells = cells[:r.cols]
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
		px := r.pad + x*cw
		fg, bg, paintBG := r.cellColors(c)
		if selHere && sel.contains(abs, x) {
			fg, bg, paintBG = r.theme.SelectionFG, r.theme.SelectionBG, true
		}
		if block && (x == cur.x || (w == 2 && x+1 == cur.x)) {
			fg, bg, paintBG = r.theme.CursorText, r.theme.Cursor, true
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
		}
		r.decorations(rc, c, px, py, w*cw, fg)
		if linkHere && x >= link.StartCol && x <= link.EndCol && c.Underline == term.UnderlineNone {
			rc.deco = append(rc.deco, rectInst{float32(px), float32(py + r.m.Underline), float32(w * cw), float32(r.m.Thickness), toRGBA(fg)})
		}
	}

	if cursorHere && !block {
		r.cursorShape(rc, cur, py)
	}
}

func (r *Renderer) decorations(rc *rowCache, c *term.Cell, px, py, width int, fg theme.RGB) {
	if c.Underline == term.UnderlineNone && c.Attrs&(term.AttrStrike|term.AttrOverline) == 0 {
		return
	}
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
		ul = toRGBA(r.resolve(c.UL, fg))
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

func (r *Renderer) cursorShape(rc *rowCache, cur cursorState, py int) {
	cw, ch := r.m.CellW, r.m.CellH
	px := r.pad + cur.x*cw
	col := toRGBA(r.theme.Cursor)
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

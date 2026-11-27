// Package render draws terminals with OpenGL 3.3.
//
// Each frame is three instanced draw calls: background rectangles, glyph
// quads sampled from a glyph atlas, and decoration rectangles (underlines,
// strikethrough, cursor shapes, separators). A window can show several
// terminals (tabs, splits); each has a Surface caching its instance data
// per row, rebuilt only for rows the terminal reports as dirty.
package render

import (
	"unsafe"

	"github.com/go-gl/gl/v3.3-core/gl"

	"github.com/zach-monroe/germ/internal/font"
	"github.com/zach-monroe/germ/internal/term"
	"github.com/zach-monroe/germ/internal/theme"
)

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
	colors   []glyphInst // color glyphs (emoji), from the color atlas
}

type cursorState struct {
	x, y    int
	visible bool
	style   term.CursorStyle
	focused bool
}

// Renderer owns the GL state, fonts, and glyph atlas shared by all
// surfaces. It must be used from the GL thread.
type Renderer struct {
	fonts *font.Set
	m     font.Metrics
	atlas *atlas
	pad   int

	rectProg, glyphProg uint32
	bgBuf, decoBuf      instanceBuffer
	glyphBuf            instanceBuffer
	uRectViewport       int32
	uGlyphViewport      int32
	uAtlasSize          int32
	colorProg           uint32
	colorBuf            instanceBuffer
	uColorViewport      int32
	uColorAtlasSize     int32

	width, height int // framebuffer size
	fontGen       int // bumped by SetFonts/SetPad so surfaces rebuild
	ligatures     bool

	bg, deco []rectInst
	glyphs   []glyphInst
	colors   []glyphInst
}

type instanceBuffer struct {
	vao, vbo uint32
	cap      int // bytes allocated on the GPU
}

// New creates a renderer. pad is the inner window padding in pixels.
func New(fonts *font.Set, pad int) (*Renderer, error) {
	r := &Renderer{fonts: fonts, m: fonts.Metrics(), pad: pad}
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
	if r.colorProg, err = compileProgram(glyphVert, colorFrag); err != nil {
		return nil, err
	}
	r.uColorViewport = uniform(r.colorProg, "uViewport")
	r.uColorAtlasSize = uniform(r.colorProg, "uAtlasSize")
	gl.UseProgram(r.colorProg)
	gl.Uniform1i(uniform(r.colorProg, "uAtlas"), 0)
	r.colorBuf = newGlyphBuffer()

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
// should recompute layouts afterwards since the cell size changes.
func (r *Renderer) SetFonts(fonts *font.Set) {
	gl.DeleteTextures(1, &r.atlas.tex)
	gl.DeleteTextures(1, &r.atlas.color.tex)
	r.fonts, r.m = fonts, fonts.Metrics()
	r.atlas = newAtlas(fonts)
	r.fontGen++
}

// SetLigatures turns programming-font ligatures on or off.
func (r *Renderer) SetLigatures(on bool) {
	if on != r.ligatures {
		r.ligatures = on
		r.fontGen++
	}
}

// CellSize returns the cell size in pixels.
func (r *Renderer) CellSize() (w, h int) { return r.m.CellW, r.m.CellH }

// SetPad changes the window padding.
func (r *Renderer) SetPad(pad int) {
	r.pad = max(0, pad)
	r.fontGen++
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

// Rect is a solid rectangle in pixels (separators, tab bar background).
type Rect struct {
	X, Y, W, H int
	Color      theme.RGB
}

// Frame draws a complete frame: the background, every surface's cached
// instances, and extra rectangles on top. The caller swaps buffers.
func (r *Renderer) Frame(bg theme.RGB, surfaces []*Surface, rects []Rect) {
	r.bg, r.deco, r.glyphs, r.colors = r.bg[:0], r.deco[:0], r.glyphs[:0], r.colors[:0]
	for _, s := range surfaces {
		for i := range s.rows {
			r.bg = append(r.bg, s.rows[i].bg...)
			r.deco = append(r.deco, s.rows[i].deco...)
			r.glyphs = append(r.glyphs, s.rows[i].glyphs...)
			r.colors = append(r.colors, s.rows[i].colors...)
		}
	}
	for _, rc := range rects {
		r.deco = append(r.deco, rectInst{float32(rc.X), float32(rc.Y), float32(rc.W), float32(rc.H), toRGBA(rc.Color)})
	}

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

	if len(r.colors) > 0 {
		c := r.atlas.color
		gl.UseProgram(r.colorProg)
		gl.Uniform2f(r.uColorViewport, vw, vh)
		gl.Uniform2f(r.uColorAtlasSize, float32(c.size), float32(c.size))
		gl.BindTexture(gl.TEXTURE_2D, c.tex)
		drawInstances(&r.colorBuf, r.colors)
	}

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

func toRGBA(c theme.RGB) rgba { return rgba{c.R, c.G, c.B, 255} }

func blend(a, b theme.RGB, t float64) theme.RGB {
	mix := func(x, y uint8) uint8 { return uint8(float64(x)*(1-t) + float64(y)*t) }
	return theme.RGB{R: mix(a.R, b.R), G: mix(a.G, b.G), B: mix(a.B, b.B)}
}

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

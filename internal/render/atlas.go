package render

import (
	"image"

	"github.com/go-gl/gl/v3.3-core/gl"

	"github.com/zach-monroe/germ/internal/font"
)

const (
	atlasInitial = 1024
	atlasMax     = 8192
	atlasPad     = 1
)

type glyphKey struct {
	r   rune
	st  font.Style
	gid uint16 // a glyph by ID (ligature part); r is 0
}

type atlasEntry struct {
	ok     bool            // false: nothing to draw (blank or missing glyph)
	color  bool            // in the color atlas (an emoji), not the mask atlas
	wipe   int             // color atlas wipes when placed (stale after a wipe)
	uv     image.Rectangle // location in the atlas
	bounds image.Rectangle // placement relative to the cell's top-left
}

// atlas is a single-channel texture of rasterized glyphs, packed into
// shelves. Glyphs are rasterized lazily on first use. A CPU copy of the
// pixels lets the texture grow without re-rasterizing.
type atlas struct {
	fonts   *font.Set
	tex     uint32
	size    int
	bpp     int // bytes per pixel: 1 (glyph masks, R8) or 4 (color, RGBA8)
	pix     []byte
	color   *atlas // the color atlas color glyphs go to (on the mask atlas)
	wipes   int    // times this atlas was wiped
	x, y, h int    // shelf packer cursor and current shelf height
	glyphs  map[glyphKey]atlasEntry
	gen     int // bumped when the atlas is wiped; cached UVs become invalid
}

func newAtlas(fonts *font.Set) *atlas {
	a := newAtlasBpp(fonts, 1, atlasInitial)
	a.color = newAtlasBpp(fonts, 4, 512)
	return a
}

func newAtlasBpp(fonts *font.Set, bpp, size int) *atlas {
	a := &atlas{fonts: fonts, bpp: bpp, glyphs: make(map[glyphKey]atlasEntry, 256)}
	gl.GenTextures(1, &a.tex)
	a.resize(size)
	return a
}

func (a *atlas) formats() (internal int32, format uint32) {
	if a.bpp == 4 {
		return gl.RGBA8, gl.RGBA
	}
	return gl.R8, gl.RED
}

func (a *atlas) resize(size int) {
	b := a.bpp
	pix := make([]byte, size*size*b)
	for y := 0; y < a.size; y++ {
		copy(pix[y*size*b:], a.pix[y*a.size*b:(y+1)*a.size*b])
	}
	a.pix, a.size = pix, size

	internal, format := a.formats()
	filter := int32(gl.NEAREST) // glyph masks are drawn 1:1
	if b == 4 {
		filter = gl.LINEAR // emoji images too, but smooth any rounding
	}
	gl.BindTexture(gl.TEXTURE_2D, a.tex)
	gl.PixelStorei(gl.UNPACK_ALIGNMENT, 1)
	gl.TexImage2D(gl.TEXTURE_2D, 0, internal, int32(size), int32(size), 0, format, gl.UNSIGNED_BYTE, gl.Ptr(a.pix))
	gl.TexParameteri(gl.TEXTURE_2D, gl.TEXTURE_MIN_FILTER, filter)
	gl.TexParameteri(gl.TEXTURE_2D, gl.TEXTURE_MAG_FILTER, filter)
	gl.TexParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_S, gl.CLAMP_TO_EDGE)
	gl.TexParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_T, gl.CLAMP_TO_EDGE)
}

// get returns the atlas entry for r, rasterizing it if needed.
func (a *atlas) get(r rune, st font.Style) atlasEntry {
	k := glyphKey{r: r, st: st}
	if e, ok := a.glyphs[k]; ok && (!e.color || e.wipe == a.color.wipes) {
		return e
	}
	e := a.add(r, st)
	a.glyphs[k] = e
	return e
}

// getGlyph returns the entry for a glyph by ID (ligatures).
func (a *atlas) getGlyph(id uint16, st font.Style) atlasEntry {
	k := glyphKey{st: st, gid: id}
	if e, ok := a.glyphs[k]; ok {
		return e
	}
	var e atlasEntry
	if g, ok := a.fonts.GlyphByID(id, st); ok {
		e = a.place(g.Bounds, g.Mask.Pix, g.Mask.Stride, &a.gen)
	}
	a.glyphs[k] = e
	return e
}

func (a *atlas) add(r rune, st font.Style) atlasEntry {
	g, ok := a.fonts.Glyph(r, st)
	if !ok {
		return atlasEntry{}
	}
	if g.Color != nil {
		if a.color == nil {
			return atlasEntry{}
		}
		e := a.color.place(g.Bounds, g.Color.Pix, g.Color.Stride, &a.gen)
		e.color, e.wipe = e.ok, a.color.wipes
		return e
	}
	return a.place(g.Bounds, g.Mask.Pix, g.Mask.Stride, &a.gen)
}

// place packs a w×h image (pix with stride bytes per row, in this atlas's
// format) and uploads it. gen is bumped if the atlas had to be wiped.
func (a *atlas) place(bounds image.Rectangle, pixels []byte, stride int, gen *int) atlasEntry {
	w, h := bounds.Dx(), bounds.Dy()
	if w+atlasPad > a.size || h+atlasPad > a.size {
		return atlasEntry{} // absurdly large glyph
	}
	if a.x+w+atlasPad > a.size { // next shelf
		a.x, a.y, a.h = 0, a.y+a.h, 0
	}
	for a.y+h+atlasPad > a.size {
		if a.size >= atlasMax {
			// Full: start over. Previously returned entries stay in
			// the map pointing at overwritten pixels, so drop them all.
			// Callers rebuild every row when this happens.
			clear(a.glyphs)
			clear(a.pix)
			a.x, a.y, a.h = 0, 0, 0
			a.wipes++
			*gen++
			a.resize(a.size)
			break
		}
		a.resize(a.size * 2)
	}

	b := a.bpp
	uv := image.Rect(a.x, a.y, a.x+w, a.y+h)
	for row := 0; row < h; row++ {
		copy(a.pix[((a.y+row)*a.size+a.x)*b:], pixels[row*stride:row*stride+w*b])
	}
	_, format := a.formats()
	gl.BindTexture(gl.TEXTURE_2D, a.tex)
	gl.PixelStorei(gl.UNPACK_ALIGNMENT, 1)
	gl.PixelStorei(gl.UNPACK_ROW_LENGTH, int32(stride/b))
	gl.TexSubImage2D(gl.TEXTURE_2D, 0, int32(a.x), int32(a.y), int32(w), int32(h), format, gl.UNSIGNED_BYTE, gl.Ptr(pixels))
	gl.PixelStorei(gl.UNPACK_ROW_LENGTH, 0)

	a.x += w + atlasPad
	a.h = max(a.h, h+atlasPad)
	return atlasEntry{ok: true, uv: uv, bounds: bounds}
}

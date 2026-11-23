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
	r  rune
	st font.Style
}

type atlasEntry struct {
	ok     bool            // false: nothing to draw (blank or missing glyph)
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
	pix     []byte
	x, y, h int // shelf packer cursor and current shelf height
	glyphs  map[glyphKey]atlasEntry
	gen     int // bumped when the atlas is wiped; cached UVs become invalid
}

func newAtlas(fonts *font.Set) *atlas {
	a := &atlas{fonts: fonts, glyphs: make(map[glyphKey]atlasEntry, 256)}
	gl.GenTextures(1, &a.tex)
	a.resize(atlasInitial)
	return a
}

func (a *atlas) resize(size int) {
	pix := make([]byte, size*size)
	for y := 0; y < a.size; y++ {
		copy(pix[y*size:], a.pix[y*a.size:(y+1)*a.size])
	}
	a.pix, a.size = pix, size

	gl.BindTexture(gl.TEXTURE_2D, a.tex)
	gl.PixelStorei(gl.UNPACK_ALIGNMENT, 1)
	gl.TexImage2D(gl.TEXTURE_2D, 0, gl.R8, int32(size), int32(size), 0, gl.RED, gl.UNSIGNED_BYTE, gl.Ptr(a.pix))
	gl.TexParameteri(gl.TEXTURE_2D, gl.TEXTURE_MIN_FILTER, gl.NEAREST)
	gl.TexParameteri(gl.TEXTURE_2D, gl.TEXTURE_MAG_FILTER, gl.NEAREST)
	gl.TexParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_S, gl.CLAMP_TO_EDGE)
	gl.TexParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_T, gl.CLAMP_TO_EDGE)
}

// get returns the atlas entry for r, rasterizing it if needed.
func (a *atlas) get(r rune, st font.Style) atlasEntry {
	k := glyphKey{r, st}
	if e, ok := a.glyphs[k]; ok {
		return e
	}
	e := a.add(r, st)
	a.glyphs[k] = e
	return e
}

func (a *atlas) add(r rune, st font.Style) atlasEntry {
	g, ok := a.fonts.Glyph(r, st)
	if !ok {
		return atlasEntry{}
	}
	w, h := g.Bounds.Dx(), g.Bounds.Dy()
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
			a.gen++
			a.resize(a.size)
			break
		}
		a.resize(a.size * 2)
	}

	uv := image.Rect(a.x, a.y, a.x+w, a.y+h)
	for row := 0; row < h; row++ {
		copy(a.pix[(a.y+row)*a.size+a.x:], g.Mask.Pix[row*g.Mask.Stride:row*g.Mask.Stride+w])
	}
	gl.BindTexture(gl.TEXTURE_2D, a.tex)
	gl.PixelStorei(gl.UNPACK_ALIGNMENT, 1)
	gl.PixelStorei(gl.UNPACK_ROW_LENGTH, int32(g.Mask.Stride))
	gl.TexSubImage2D(gl.TEXTURE_2D, 0, int32(a.x), int32(a.y), int32(w), int32(h), gl.RED, gl.UNSIGNED_BYTE, gl.Ptr(g.Mask.Pix))
	gl.PixelStorei(gl.UNPACK_ROW_LENGTH, 0)

	a.x += w + atlasPad
	a.h = max(a.h, h+atlasPad)
	return atlasEntry{ok: true, uv: uv, bounds: g.Bounds}
}

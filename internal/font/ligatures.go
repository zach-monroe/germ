package font

import (
	"bytes"
	"image"
	"math"
	"os"

	"github.com/go-text/typesetting/di"
	gotext "github.com/go-text/typesetting/font"
	"github.com/go-text/typesetting/language"
	"github.com/go-text/typesetting/shaping"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
	"golang.org/x/image/vector"
)

// Ligatures, the way programming fonts (JetBrains Mono, Fira Code,
// Cascadia Code, ...) do them: contextual alternates that keep one glyph
// per character, so "->" is still two glyphs that together draw an arrow.
// That keeps the terminal grid: Ligatures shapes a run of text and, when
// the font swapped glyphs one for one, reports which glyph each cell
// should draw instead. Shapings that merge characters into fewer glyphs
// are ignored (the cells keep their own characters).

type shaper struct {
	face *gotext.Face
	hb   shaping.HarfbuzzShaper
	bad  bool // couldn't parse
}

type ligKey struct {
	text string
	st   Style
}

const ligCacheMax = 4096

func (f *face) shaper() *shaper {
	if f.shp != nil {
		return f.shp
	}
	f.shp = &shaper{bad: true}
	data, err := os.ReadFile(f.path)
	if err != nil {
		return f.shp
	}
	faces, err := gotext.ParseTTC(bytes.NewReader(data))
	if err != nil || len(faces) == 0 {
		return f.shp
	}
	f.shp = &shaper{face: faces[min(f.idx, len(faces)-1)]}
	return f.shp
}

// IsLigatureRune reports whether r can take part in a ligature (worth
// shaping): ASCII punctuation, which is what coding ligatures are made of.
func IsLigatureRune(r rune) bool {
	return r < 0x80 && r > ' ' && !(r >= '0' && r <= '9') && !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z')
}

// Ligatures shapes text in style st and returns the glyph each rune
// should be drawn with, or nil if the font changes nothing (or the
// result doesn't map one glyph per rune).
func (s *Set) Ligatures(text []rune, st Style) []uint16 {
	if len(text) < 2 {
		return nil
	}
	k := ligKey{string(text), st}
	if v, ok := s.ligs[k]; ok {
		return v
	}
	v := s.shape(text, st)
	if len(s.ligs) >= ligCacheMax {
		clear(s.ligs)
	}
	s.ligs[k] = v
	return v
}

func (s *Set) shape(text []rune, st Style) []uint16 {
	f := s.faces[st]
	for _, r := range text {
		if !f.has(r) {
			return nil
		}
	}
	sh := f.shaper()
	if sh.bad {
		return nil
	}
	out := sh.hb.Shape(shaping.Input{
		Text: text, RunStart: 0, RunEnd: len(text),
		Direction: di.DirectionLTR, Face: sh.face,
		Size:   fixed.Int26_6(s.sizePx * 64),
		Script: language.Latin, Language: language.NewLanguage("en"),
	})
	if len(out.Glyphs) != len(text) {
		return nil
	}
	ids := make([]uint16, len(text))
	changed := false
	for i, g := range out.Glyphs {
		if g.ClusterIndex != i || g.RuneCount != 1 || g.GlyphCount != 1 {
			return nil
		}
		ids[i] = uint16(g.GlyphID)
		if nom, ok := sh.face.NominalGlyph(text[i]); !ok || uint16(nom) != ids[i] {
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return ids
}

// GlyphByID rasterizes glyph id of style st's face (a ligature part).
func (s *Set) GlyphByID(id uint16, st Style) (Glyph, bool) {
	f := s.faces[st]
	segs, err := f.sf.LoadGlyph(&f.buf, sfnt.GlyphIndex(id), fixed.Int26_6(s.sizePx*64), nil)
	if err != nil || len(segs) == 0 {
		return Glyph{}, false
	}
	base := float32(s.metrics.Baseline)
	minX, minY := float32(math.Inf(1)), float32(math.Inf(1))
	maxX, maxY := float32(math.Inf(-1)), float32(math.Inf(-1))
	for _, sg := range segs {
		n := map[sfnt.SegmentOp]int{sfnt.SegmentOpMoveTo: 1, sfnt.SegmentOpLineTo: 1, sfnt.SegmentOpQuadTo: 2, sfnt.SegmentOpCubeTo: 3}[sg.Op]
		for _, p := range sg.Args[:n] {
			x, y := float32(p.X)/64, float32(p.Y)/64+base
			minX, maxX = min(minX, x), max(maxX, x)
			minY, maxY = min(minY, y), max(maxY, y)
		}
	}
	b := image.Rect(int(math.Floor(float64(minX))), int(math.Floor(float64(minY))),
		int(math.Ceil(float64(maxX))), int(math.Ceil(float64(maxY))))
	if b.Empty() {
		return Glyph{}, false
	}
	ox, oy := float32(b.Min.X), float32(b.Min.Y)
	pt := func(p fixed.Point26_6) (float32, float32) {
		return float32(p.X)/64 - ox, float32(p.Y)/64 + base - oy
	}
	r := vector.NewRasterizer(b.Dx(), b.Dy())
	for _, sg := range segs {
		switch sg.Op {
		case sfnt.SegmentOpMoveTo:
			r.MoveTo(pt(sg.Args[0]))
		case sfnt.SegmentOpLineTo:
			r.LineTo(pt(sg.Args[0]))
		case sfnt.SegmentOpQuadTo:
			x1, y1 := pt(sg.Args[0])
			x2, y2 := pt(sg.Args[1])
			r.QuadTo(x1, y1, x2, y2)
		case sfnt.SegmentOpCubeTo:
			x1, y1 := pt(sg.Args[0])
			x2, y2 := pt(sg.Args[1])
			x3, y3 := pt(sg.Args[2])
			r.CubeTo(x1, y1, x2, y2, x3, y3)
		}
	}
	r.ClosePath()
	mask := image.NewAlpha(image.Rect(0, 0, b.Dx(), b.Dy()))
	r.Draw(mask, mask.Bounds(), image.Opaque, image.Point{})
	if s.syntheticBold(st) {
		mask, b = embolden(mask, b, max(1, int(s.sizePx/18)))
	}
	if s.syntheticItalic(st) {
		mask, b = slant(mask, b, s.metrics.Baseline)
	}
	return Glyph{Mask: mask, Bounds: b}, true
}

// Package font finds fonts with fontconfig, computes cell metrics, and
// rasterizes glyphs to alpha masks. One glyph per cell; no shaping (so no
// ligatures) in v1.
//
// A Set is not safe for concurrent use; the renderer uses it from the
// main thread only.
package font

import (
	"fmt"
	"image"
	"math"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"

	"github.com/golang/freetype/truetype"
	xfont "golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
)

// Style selects a face within a Set.
type Style uint8

const (
	Regular Style = iota
	Bold
	Italic
	BoldItalic
)

// Metrics are the dimensions of one terminal cell, in pixels.
type Metrics struct {
	CellW, CellH int
	Baseline     int // distance from the cell top to the baseline
	Underline    int // underline position below the cell top
	Thickness    int // stroke thickness for underline/strikethrough
}

// Glyph is a rasterized glyph.
type Glyph struct {
	Mask *image.Alpha
	// Color, instead of Mask, is a full-color glyph (emoji), drawn as is.
	Color *image.NRGBA
	// Bounds is where Mask goes relative to the cell's top-left corner.
	Bounds image.Rectangle
}

type face struct {
	sf   *sfnt.Font
	face xfont.Face // x/image rasterizer: handles every glyph
	buf  sfnt.Buffer
	// hinted is the golang/freetype rasterizer (TrueType only), used when
	// it can map the rune: its cmap support is narrower than x/image's.
	tt     *truetype.Font
	hinted xfont.Face
}

// rasterizer picks the face to draw r with.
func (f *face) rasterizer(r rune) xfont.Face {
	if f.tt != nil && f.tt.Index(r) != 0 {
		return f.hinted
	}
	return f.face
}

func (f *face) has(r rune) bool {
	i, err := f.sf.GlyphIndex(&f.buf, r)
	return err == nil && i != 0
}

// Set is a family of faces at one size plus fallbacks for missing glyphs.
type Set struct {
	faces    [4]*face
	sizePx   float64
	metrics  Metrics
	byFile   map[string]*face // loaded fallback faces
	fallback map[rune]*face   // nil entry = no font has it
	emoji    *emojiFont       // color emoji, loaded on first use
}

// Load finds family via fontconfig and opens it at size points for the
// given DPI (96 * content scale).
func Load(family string, sizePt, dpi float64) (*Set, error) {
	s := &Set{
		emoji:    &emojiFont{},
		sizePx:   sizePt * dpi / 72,
		byFile:   map[string]*face{},
		fallback: map[rune]*face{},
	}
	styles := [4]string{"regular", "bold", "italic", "bold:italic"}
	// Look the four styles up in parallel (each fc-match is ~20 ms when
	// not cached).
	type found struct {
		path string
		idx  int
		err  error
	}
	var res [4]found
	var wg sync.WaitGroup
	for i, st := range styles {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p, idx, err := match(family + ":" + st)
			res[i] = found{p, idx, err}
		}()
	}
	wg.Wait()
	for i := range styles {
		path, idx, err := res[i].path, res[i].idx, res[i].err
		if err != nil {
			if i == 0 {
				return nil, err
			}
			s.faces[i] = s.faces[0]
			continue
		}
		f, err := s.open(path, idx)
		if err != nil {
			if i == 0 {
				return nil, err
			}
			f = s.faces[0]
		}
		s.faces[i] = f
	}
	s.metrics = computeMetrics(s.faces[0])
	return s, nil
}

func computeMetrics(f *face) Metrics {
	m := f.face.Metrics()
	adv, _ := f.face.GlyphAdvance('M')
	asc := m.Ascent.Ceil()
	desc := m.Descent.Ceil()
	cellH := max(asc+desc, m.Height.Ceil())
	thick := max(1, int(math.Round(float64(cellH)/16)))
	return Metrics{
		CellW:     max(1, adv.Round()),
		CellH:     cellH,
		Baseline:  asc + (cellH-asc-desc)/2,
		Underline: min(cellH-thick, asc+(cellH-asc-desc)/2+max(1, desc/3)),
		Thickness: thick,
	}
}

// Metrics returns the cell metrics.
func (s *Set) Metrics() Metrics { return s.metrics }

// Glyph rasterizes r in the given style. ok is false if no font can draw it
// (e.g. color-only emoji fonts, which are not supported yet).
func (s *Set) Glyph(r rune, st Style) (Glyph, bool) {
	if r >= 0x2500 && r <= 0x259F {
		if g, ok := s.builtinGlyph(r); ok {
			return g, true
		}
	}
	if isEmoji(r) {
		if g, ok := s.colorGlyph(r); ok {
			return g, true
		}
	}
	f := s.faces[st]
	if !f.has(r) {
		if f = s.fallbackFor(r); f == nil {
			return s.colorGlyph(r) // a symbol only the emoji font has
		}
	}
	dot := fixed.P(0, s.metrics.Baseline)
	dr, mask, mp, _, ok := f.rasterizer(r).Glyph(dot, r)
	if !ok || dr.Empty() {
		// A font can map a character it has no outline for (a color
		// font's bitmap-only glyph, as fontconfig sees it): try color.
		return s.colorGlyph(r)
	}
	am, isAlpha := mask.(*image.Alpha)
	if !isAlpha || mp != (image.Point{}) {
		// Normalize to a tightly packed *image.Alpha at the origin.
		cp := image.NewAlpha(image.Rect(0, 0, dr.Dx(), dr.Dy()))
		for y := 0; y < dr.Dy(); y++ {
			for x := 0; x < dr.Dx(); x++ {
				_, _, _, a := mask.At(mp.X+x, mp.Y+y).RGBA()
				cp.Pix[y*cp.Stride+x] = uint8(a >> 8)
			}
		}
		am = cp
	} else {
		// The face reuses its mask buffer between calls; copy it.
		cp := image.NewAlpha(am.Rect)
		copy(cp.Pix, am.Pix)
		am = cp
	}
	return Glyph{Mask: am, Bounds: dr}, true
}

func (s *Set) fallbackFor(r rune) *face {
	if f, ok := s.fallback[r]; ok {
		return f
	}
	var found *face
	path, idx, err := match(fmt.Sprintf(":charset=%x", r))
	if err == nil {
		if f, err := s.open(path, idx); err == nil && f.has(r) {
			found = f
		}
	}
	s.fallback[r] = found
	return found
}

func (s *Set) open(path string, idx int) (*face, error) {
	key := path + "#" + strconv.Itoa(idx)
	if f, ok := s.byFile[key]; ok {
		return f, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var sf *sfnt.Font
	if c, err := opentype.ParseCollection(data); err == nil && c.NumFonts() > 1 {
		sf, err = c.Font(min(idx, c.NumFonts()-1))
		if err != nil {
			return nil, err
		}
	} else if sf, err = opentype.Parse(data); err != nil {
		return nil, fmt.Errorf("font: parse %s: %w", path, err)
	}
	// Prefer golang/freetype for TrueType outlines: it runs the font's
	// hinting bytecode, which snaps stems to whole pixels so text is crisp
	// (x/image/font/opentype ignores hinting instructions entirely). CFF
	// fonts and collections fall back to x/image.
	xf, err := opentype.NewFace(sf, &opentype.FaceOptions{Size: s.sizePx, DPI: 72, Hinting: xfont.HintingFull})
	if err != nil {
		return nil, err
	}
	f := &face{sf: sf, face: xf}
	if tf, err := truetype.Parse(data); err == nil {
		f.tt = tf
		f.hinted = truetype.NewFace(tf, &truetype.Options{Size: s.sizePx, DPI: 72, Hinting: xfont.HintingFull})
	}
	s.byFile[key] = f
	return f, nil
}

// match asks fontconfig for the best file for a pattern.
func match(pattern string) (path string, index int, err error) {
	if m, ok := matchCache.get(pattern); ok {
		return m.Path, m.Index, nil
	}
	path, index, err = fcMatch(pattern)
	if err == nil {
		matchCache.put(pattern, cachedMatch{path, index})
	}
	return path, index, err
}

// fcMatch asks fontconfig (the fc-match tool).
func fcMatch(pattern string) (path string, index int, err error) {
	out, err := exec.Command("fc-match", "-f", "%{file}\n%{index}", pattern).Output()
	if err != nil {
		return "", 0, fmt.Errorf("font: fc-match %q: %w", pattern, err)
	}
	parts := strings.SplitN(string(out), "\n", 2)
	if parts[0] == "" {
		return "", 0, fmt.Errorf("font: no match for %q", pattern)
	}
	if len(parts) == 2 {
		index, _ = strconv.Atoi(strings.TrimSpace(parts[1]))
	}
	return parts[0], index, nil
}

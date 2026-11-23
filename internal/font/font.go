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
	// Bounds is where Mask goes relative to the cell's top-left corner.
	Bounds image.Rectangle
}

type face struct {
	sf   *sfnt.Font
	face xfont.Face
	buf  sfnt.Buffer
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
}

// Load finds family via fontconfig and opens it at size points for the
// given DPI (96 * content scale).
func Load(family string, sizePt, dpi float64) (*Set, error) {
	s := &Set{
		sizePx:   sizePt * dpi / 72,
		byFile:   map[string]*face{},
		fallback: map[rune]*face{},
	}
	styles := [4]string{"regular", "bold", "italic", "bold:italic"}
	for i, st := range styles {
		path, idx, err := match(family + ":" + st)
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
	f := s.faces[st]
	if !f.has(r) {
		if f = s.fallbackFor(r); f == nil {
			return Glyph{}, false
		}
	}
	dot := fixed.P(0, s.metrics.Baseline)
	dr, mask, mp, _, ok := f.face.Glyph(dot, r)
	if !ok || dr.Empty() {
		return Glyph{}, false
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
	xf, err := opentype.NewFace(sf, &opentype.FaceOptions{Size: s.sizePx, DPI: 72, Hinting: xfont.HintingFull})
	if err != nil {
		return nil, err
	}
	f := &face{sf: sf, face: xf}
	s.byFile[key] = f
	return f, nil
}

// match asks fontconfig for the best file for a pattern.
func match(pattern string) (path string, index int, err error) {
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

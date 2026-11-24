package font

import (
	"bytes"
	"image"
	"image/png"
	"os"
	"sync"

	gotext "github.com/go-text/typesetting/font"
	xdraw "golang.org/x/image/draw"
)

// Color emoji. Fonts like Noto Color Emoji store each emoji as a PNG
// (CBDT/sbix tables), which the outline rasterizers can't draw. germ reads
// the bitmap with go-text, scales it into the emoji's two cells, and hands
// back a full-color glyph (Glyph.Color) for the renderer's color atlas.

type emojiFont struct {
	once sync.Once
	face *gotext.Face
}

// isEmoji reports whether r is drawn as a color emoji by default: the
// pictographs, plus the older characters Unicode gives emoji presentation
// (✅ ⭐ ⌛ ❌ …). Other symbols that double as text (★ ✓ ☀ →) use a text
// font when one has them, color only as a fallback.
func isEmoji(r rune) bool {
	if r >= 0x1F000 && r <= 0x1FAFF {
		return true
	}
	for _, rg := range emojiPresentation {
		if r >= rg[0] && r <= rg[1] {
			return true
		}
	}
	return false
}

// emojiPresentation: characters below U+1F000 with Emoji_Presentation=Yes
// (Unicode emoji-data.txt).
var emojiPresentation = [][2]rune{
	{0x231A, 0x231B}, {0x23E9, 0x23EC}, {0x23F0, 0x23F0}, {0x23F3, 0x23F3},
	{0x25FD, 0x25FE}, {0x2614, 0x2615}, {0x2648, 0x2653}, {0x267F, 0x267F},
	{0x2693, 0x2693}, {0x26A1, 0x26A1}, {0x26AA, 0x26AB}, {0x26BD, 0x26BE},
	{0x26C4, 0x26C5}, {0x26CE, 0x26CE}, {0x26D4, 0x26D4}, {0x26EA, 0x26EA},
	{0x26F2, 0x26F3}, {0x26F5, 0x26F5}, {0x26FA, 0x26FA}, {0x26FD, 0x26FD},
	{0x2705, 0x2705}, {0x270A, 0x270B}, {0x2728, 0x2728}, {0x274C, 0x274C},
	{0x274E, 0x274E}, {0x2753, 0x2755}, {0x2757, 0x2757}, {0x2795, 0x2797},
	{0x27B0, 0x27B0}, {0x27BF, 0x27BF}, {0x2B1B, 0x2B1C}, {0x2B50, 0x2B50},
	{0x2B55, 0x2B55},
}

func (s *Set) emojiFace() *gotext.Face {
	s.emoji.once.Do(func() {
		path, idx, err := match("emoji:color=true")
		if err != nil {
			return
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return
		}
		faces, err := gotext.ParseTTC(bytes.NewReader(data))
		if err != nil || idx >= len(faces) {
			return
		}
		f := faces[idx]
		f.SetPpem(512, 512) // ask for the largest bitmap strike; we scale down
		s.emoji.face = f
	})
	return s.emoji.face
}

// colorGlyph renders r from the color emoji font, sized to two cells.
func (s *Set) colorGlyph(r rune) (Glyph, bool) {
	f := s.emojiFace()
	if f == nil {
		return Glyph{}, false
	}
	gid, ok := f.NominalGlyph(r)
	if !ok {
		return Glyph{}, false
	}
	bm, ok := f.GlyphDataBitmap(gid)
	if !ok || bm.Format != gotext.PNG {
		return Glyph{}, false
	}
	src, err := png.Decode(bytes.NewReader(bm.Data))
	if err != nil || src.Bounds().Empty() {
		return Glyph{}, false
	}
	// Fit into two cells, keeping the aspect ratio, a little inset.
	cw, ch := s.metrics.CellW, s.metrics.CellH
	boxW, boxH := float64(2*cw), float64(ch)*0.95
	sw, sh := float64(src.Bounds().Dx()), float64(src.Bounds().Dy())
	scale := min(boxW/sw, boxH/sh)
	w, h := max(1, int(sw*scale+0.5)), max(1, int(sh*scale+0.5))
	dst := image.NewNRGBA(image.Rect(0, 0, w, h))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, src.Bounds(), xdraw.Src, nil)
	x0 := (2*cw - w) / 2
	y0 := (ch - h) / 2
	return Glyph{Color: dst, Bounds: image.Rect(x0, y0, x0+w, y0+h)}, true
}

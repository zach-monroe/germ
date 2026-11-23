package term

import "github.com/zach-monroe/germ/internal/vt"

// sgr applies Select Graphic Rendition parameters to the pen.
func (t *Terminal) sgr(p *vt.Params) {
	pen := &t.cur.Pen
	if p.Len() == 0 {
		*pen = Cell{}
		return
	}
	for i := 0; i < p.Len(); i++ {
		v := p.Raw(i)
		// Count colon sub-parameters attached to this one.
		j := i + 1
		for j < p.Len() && p.IsSub(j) {
			j++
		}
		subs := j - i - 1

		switch {
		case v == 0:
			*pen = Cell{}
		case v == 1:
			pen.Attrs |= AttrBold
		case v == 2:
			pen.Attrs |= AttrFaint
		case v == 3:
			pen.Attrs |= AttrItalic
		case v == 4:
			pen.Underline = UnderlineSingle
			if subs > 0 {
				if s := p.Raw(i + 1); s <= uint16(UnderlineDashed) {
					pen.Underline = Underline(s)
				}
			}
		case v == 5 || v == 6:
			pen.Attrs |= AttrBlink
		case v == 7:
			pen.Attrs |= AttrInverse
		case v == 8:
			pen.Attrs |= AttrHidden
		case v == 9:
			pen.Attrs |= AttrStrike
		case v == 21:
			pen.Underline = UnderlineDouble
		case v == 22:
			pen.Attrs &^= AttrBold | AttrFaint
		case v == 23:
			pen.Attrs &^= AttrItalic
		case v == 24:
			pen.Underline = UnderlineNone
		case v == 25:
			pen.Attrs &^= AttrBlink
		case v == 27:
			pen.Attrs &^= AttrInverse
		case v == 28:
			pen.Attrs &^= AttrHidden
		case v == 29:
			pen.Attrs &^= AttrStrike
		case v >= 30 && v <= 37:
			pen.FG = PaletteColor(uint8(v - 30))
		case v == 38, v == 48, v == 58:
			c, skip, ok := extColor(p, i, subs)
			if ok {
				switch v {
				case 38:
					pen.FG = c
				case 48:
					pen.BG = c
				case 58:
					pen.UL = c
				}
			}
			i += skip
		case v == 39:
			pen.FG = DefaultColor
		case v >= 40 && v <= 47:
			pen.BG = PaletteColor(uint8(v - 40))
		case v == 49:
			pen.BG = DefaultColor
		case v == 53:
			pen.Attrs |= AttrOverline
		case v == 55:
			pen.Attrs &^= AttrOverline
		case v == 59:
			pen.UL = DefaultColor
		case v >= 90 && v <= 97:
			pen.FG = PaletteColor(uint8(v - 90 + 8))
		case v >= 100 && v <= 107:
			pen.BG = PaletteColor(uint8(v - 100 + 8))
		}
		i += subs
	}
}

// extColor parses an extended color starting at parameter i (38/48/58).
// It supports both the colon form ("38:5:n", "38:2::r:g:b", "38:2:r:g:b")
// and the legacy semicolon form ("38;5;n", "38;2;r;g;b"). skip is how many
// following semicolon parameters were consumed.
func extColor(p *vt.Params, i, subs int) (c Color, skip int, ok bool) {
	if subs > 0 {
		switch p.Raw(i + 1) {
		case 5:
			if subs >= 2 {
				return PaletteColor(uint8(p.Raw(i + 2))), 0, true
			}
		case 2:
			if subs >= 5 { // 38:2:<colorspace>:r:g:b
				return rgb(p, i+3), 0, true
			}
			if subs == 4 { // 38:2:r:g:b
				return rgb(p, i+2), 0, true
			}
		}
		return 0, 0, false
	}
	switch p.Raw(i + 1) {
	case 5:
		if i+2 < p.Len() {
			return PaletteColor(uint8(p.Raw(i + 2))), 2, true
		}
	case 2:
		if i+4 < p.Len() {
			return rgb(p, i+2), 4, true
		}
	}
	return 0, p.Len() - i - 1, false // malformed: skip the rest
}

func rgb(p *vt.Params, i int) Color {
	return RGBColor(uint8(p.Raw(i)), uint8(p.Raw(i+1)), uint8(p.Raw(i+2)))
}

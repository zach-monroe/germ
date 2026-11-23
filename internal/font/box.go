package font

import "image"

// Box-drawing (U+2500–U+257F) and block elements (U+2580–U+259F) are drawn
// procedurally at exactly the cell size, so lines join seamlessly across
// cells regardless of the font's own glyphs. tmux pane borders depend on it.

// Arm weights.
const (
	none   = 0
	light  = 1
	heavy  = 2
	double = 3
)

// arms describes a box character as weights for up, down, left, right.
type arms struct{ u, d, l, r uint8 }

var boxTable = map[rune]arms{
	0x2500: {0, 0, 1, 1}, 0x2501: {0, 0, 2, 2}, 0x2502: {1, 1, 0, 0}, 0x2503: {2, 2, 0, 0},
	// Dashed variants are drawn solid.
	0x2504: {0, 0, 1, 1}, 0x2505: {0, 0, 2, 2}, 0x2506: {1, 1, 0, 0}, 0x2507: {2, 2, 0, 0},
	0x2508: {0, 0, 1, 1}, 0x2509: {0, 0, 2, 2}, 0x250A: {1, 1, 0, 0}, 0x250B: {2, 2, 0, 0},
	0x254C: {0, 0, 1, 1}, 0x254D: {0, 0, 2, 2}, 0x254E: {1, 1, 0, 0}, 0x254F: {2, 2, 0, 0},
	// Corners.
	0x250C: {0, 1, 0, 1}, 0x250D: {0, 1, 0, 2}, 0x250E: {0, 2, 0, 1}, 0x250F: {0, 2, 0, 2},
	0x2510: {0, 1, 1, 0}, 0x2511: {0, 1, 2, 0}, 0x2512: {0, 2, 1, 0}, 0x2513: {0, 2, 2, 0},
	0x2514: {1, 0, 0, 1}, 0x2515: {1, 0, 0, 2}, 0x2516: {2, 0, 0, 1}, 0x2517: {2, 0, 0, 2},
	0x2518: {1, 0, 1, 0}, 0x2519: {1, 0, 2, 0}, 0x251A: {2, 0, 1, 0}, 0x251B: {2, 0, 2, 0},
	// Tees.
	0x251C: {1, 1, 0, 1}, 0x251D: {1, 1, 0, 2}, 0x251E: {2, 1, 0, 1}, 0x251F: {1, 2, 0, 1},
	0x2520: {2, 2, 0, 1}, 0x2521: {2, 1, 0, 2}, 0x2522: {1, 2, 0, 2}, 0x2523: {2, 2, 0, 2},
	0x2524: {1, 1, 1, 0}, 0x2525: {1, 1, 2, 0}, 0x2526: {2, 1, 1, 0}, 0x2527: {1, 2, 1, 0},
	0x2528: {2, 2, 1, 0}, 0x2529: {2, 1, 2, 0}, 0x252A: {1, 2, 2, 0}, 0x252B: {2, 2, 2, 0},
	0x252C: {0, 1, 1, 1}, 0x252D: {0, 1, 2, 1}, 0x252E: {0, 1, 1, 2}, 0x252F: {0, 1, 2, 2},
	0x2530: {0, 2, 1, 1}, 0x2531: {0, 2, 2, 1}, 0x2532: {0, 2, 1, 2}, 0x2533: {0, 2, 2, 2},
	0x2534: {1, 0, 1, 1}, 0x2535: {1, 0, 2, 1}, 0x2536: {1, 0, 1, 2}, 0x2537: {1, 0, 2, 2},
	0x2538: {2, 0, 1, 1}, 0x2539: {2, 0, 2, 1}, 0x253A: {2, 0, 1, 2}, 0x253B: {2, 0, 2, 2},
	// Crosses.
	0x253C: {1, 1, 1, 1}, 0x253D: {1, 1, 2, 1}, 0x253E: {1, 1, 1, 2}, 0x253F: {1, 1, 2, 2},
	0x2540: {2, 1, 1, 1}, 0x2541: {1, 2, 1, 1}, 0x2542: {2, 2, 1, 1}, 0x2543: {2, 1, 2, 1},
	0x2544: {2, 1, 1, 2}, 0x2545: {1, 2, 2, 1}, 0x2546: {1, 2, 1, 2}, 0x2547: {2, 1, 2, 2},
	0x2548: {1, 2, 2, 2}, 0x2549: {2, 2, 2, 1}, 0x254A: {2, 2, 1, 2}, 0x254B: {2, 2, 2, 2},
	// Double lines.
	0x2550: {0, 0, 3, 3}, 0x2551: {3, 3, 0, 0},
	0x2552: {0, 1, 0, 3}, 0x2553: {0, 3, 0, 1}, 0x2554: {0, 3, 0, 3},
	0x2555: {0, 1, 3, 0}, 0x2556: {0, 3, 1, 0}, 0x2557: {0, 3, 3, 0},
	0x2558: {1, 0, 0, 3}, 0x2559: {3, 0, 0, 1}, 0x255A: {3, 0, 0, 3},
	0x255B: {1, 0, 3, 0}, 0x255C: {3, 0, 1, 0}, 0x255D: {3, 0, 3, 0},
	0x255E: {1, 1, 0, 3}, 0x255F: {3, 3, 0, 1}, 0x2560: {3, 3, 0, 3},
	0x2561: {1, 1, 3, 0}, 0x2562: {3, 3, 1, 0}, 0x2563: {3, 3, 3, 0},
	0x2564: {0, 1, 3, 3}, 0x2565: {0, 3, 1, 1}, 0x2566: {0, 3, 3, 3},
	0x2567: {1, 0, 3, 3}, 0x2568: {3, 0, 1, 1}, 0x2569: {3, 0, 3, 3},
	0x256A: {1, 1, 3, 3}, 0x256B: {3, 3, 1, 1}, 0x256C: {3, 3, 3, 3},
	// Rounded corners (drawn square for now).
	0x256D: {0, 1, 0, 1}, 0x256E: {0, 1, 1, 0}, 0x256F: {1, 0, 1, 0}, 0x2570: {1, 0, 0, 1},
	// Half lines.
	0x2574: {0, 0, 1, 0}, 0x2575: {1, 0, 0, 0}, 0x2576: {0, 0, 0, 1}, 0x2577: {0, 1, 0, 0},
	0x2578: {0, 0, 2, 0}, 0x2579: {2, 0, 0, 0}, 0x257A: {0, 0, 0, 2}, 0x257B: {0, 2, 0, 0},
	0x257C: {0, 0, 1, 2}, 0x257D: {1, 2, 0, 0}, 0x257E: {0, 0, 2, 1}, 0x257F: {2, 1, 0, 0},
}

// builtinGlyph draws box-drawing and block characters. ok is false for
// runes it does not handle.
func (s *Set) builtinGlyph(r rune) (Glyph, bool) {
	w, h := s.metrics.CellW, s.metrics.CellH
	if r >= 0x2580 && r <= 0x259F {
		return blockGlyph(r, w, h)
	}
	a, ok := boxTable[r]
	if !ok {
		return Glyph{}, false
	}
	m := image.NewAlpha(image.Rect(0, 0, w, h))
	fill := func(x0, y0, x1, y1 int) {
		x0, y0 = max(x0, 0), max(y0, 0)
		x1, y1 = min(x1, w), min(y1, h)
		for y := y0; y < y1; y++ {
			for x := x0; x < x1; x++ {
				m.Pix[y*m.Stride+x] = 0xff
			}
		}
	}

	t := max(1, s.metrics.Thickness)
	thick := func(weight uint8) int {
		if weight == heavy {
			return 2 * t
		}
		return t
	}
	cx, cy := w/2, h/2
	gap := t // half-distance between the strokes of a double line

	// Vertical arms are centred on cx, horizontal arms on cy. Each arm
	// extends past the centre by half the crossing stroke so joints are
	// filled.
	hReach := max(thick(a.l), thick(a.r)) // how far vertical arms must reach
	vReach := max(thick(a.u), thick(a.d))
	if a.l == double || a.r == double {
		hReach = gap + t
	}
	if a.u == double || a.d == double {
		vReach = gap + t
	}

	vert := func(weight uint8, y0, y1 int) {
		switch weight {
		case light, heavy:
			tw := thick(weight)
			fill(cx-tw/2, y0, cx-tw/2+tw, y1)
		case double:
			fill(cx-gap-t/2-t+1, y0, cx-gap-t/2+1, y1)
			fill(cx+gap-t/2, y0, cx+gap-t/2+t, y1)
		}
	}
	horiz := func(weight uint8, x0, x1 int) {
		switch weight {
		case light, heavy:
			tw := thick(weight)
			fill(x0, cy-tw/2, x1, cy-tw/2+tw)
		case double:
			fill(x0, cy-gap-t/2-t+1, x1, cy-gap-t/2+1)
			fill(x0, cy+gap-t/2, x1, cy+gap-t/2+t)
		}
	}
	vert(a.u, 0, cy+hReach/2+1)
	vert(a.d, cy-hReach/2, h)
	horiz(a.l, 0, cx+vReach/2+1)
	horiz(a.r, cx-vReach/2, w)
	return Glyph{Mask: m, Bounds: m.Rect}, true
}

func blockGlyph(r rune, w, h int) (Glyph, bool) {
	m := image.NewAlpha(image.Rect(0, 0, w, h))
	fill := func(x0, y0, x1, y1 int, a uint8) {
		for y := y0; y < y1; y++ {
			for x := x0; x < x1; x++ {
				m.Pix[y*m.Stride+x] = a
			}
		}
	}
	eighthY := func(n int) int { return (h*n + 4) / 8 }
	eighthX := func(n int) int { return (w*n + 4) / 8 }
	switch {
	case r == 0x2580: // ▀ upper half
		fill(0, 0, w, h/2, 0xff)
	case r >= 0x2581 && r <= 0x2588: // ▁..█ lower n/8
		n := int(r-0x2581) + 1
		fill(0, h-eighthY(n), w, h, 0xff)
	case r >= 0x2589 && r <= 0x258F: // ▉..▏ left n/8 (7/8 down to 1/8)
		n := 8 - int(r-0x2588)
		fill(0, 0, eighthX(n), h, 0xff)
	case r == 0x2590: // ▐ right half
		fill(w/2, 0, w, h, 0xff)
	case r >= 0x2591 && r <= 0x2593: // ░▒▓ shades
		fill(0, 0, w, h, uint8(0x40*(r-0x2590)))
	case r == 0x2594: // ▔ upper 1/8
		fill(0, 0, w, eighthY(1), 0xff)
	case r == 0x2595: // ▕ right 1/8
		fill(w-eighthX(1), 0, w, h, 0xff)
	default: // quadrants U+2596–U+259F
		q := map[rune][4]bool{ // UL, UR, LL, LR
			0x2596: {false, false, true, false}, 0x2597: {false, false, false, true},
			0x2598: {true, false, false, false}, 0x2599: {true, false, true, true},
			0x259A: {true, false, false, true}, 0x259B: {true, true, true, false},
			0x259C: {true, true, false, true}, 0x259D: {false, true, false, false},
			0x259E: {false, true, true, false}, 0x259F: {false, true, true, true},
		}[r]
		if q[0] {
			fill(0, 0, w/2, h/2, 0xff)
		}
		if q[1] {
			fill(w/2, 0, w, h/2, 0xff)
		}
		if q[2] {
			fill(0, h/2, w/2, h, 0xff)
		}
		if q[3] {
			fill(w/2, h/2, w, h, 0xff)
		}
	}
	return Glyph{Mask: m, Bounds: m.Rect}, true
}

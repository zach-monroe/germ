package app

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/go-gl/glfw/v3.3/glfw"

	"github.com/zach-monroe/germ/internal/render"
	"github.com/zach-monroe/germ/internal/term"
)

// Scrollback search (Ctrl+Shift+F): a search bar on the active pane's
// bottom row; matches anywhere in the pane's history are highlighted, and
// Enter / Shift+Enter (or Up / Down) step through them, scrolling the view.
// Case-insensitive unless the query has a capital letter.

type searchMatch struct {
	startRow int64
	startCol int
	endRow   int64 // a match can run across a soft-wrapped line
	endCol   int
}

type searchState struct {
	pane    *pane
	query   []rune
	matches []searchMatch // oldest first
	cur     int           // index into matches, -1 if none
	gen     int           // bumped on every change (render.View.*Gen)
	capped  bool          // stopped at maxMatches
}

const maxMatches = 5000

// openSearch starts (or focuses) the search bar on the active pane.
func (a *App) openSearch() bool {
	p := a.active()
	if p == nil {
		return false
	}
	if a.search != nil && a.search.pane == p {
		return true
	}
	a.search = &searchState{pane: p, cur: -1}
	a.forceRedraw = true
	return true
}

func (a *App) closeSearch() {
	if a.search != nil {
		a.search = nil
		a.forceRedraw = true
	}
}

// searchKey handles a key press while searching; it consumes every key so
// nothing reaches the program in the pane.
func (a *App) searchKey(key glfw.Key, mods glfw.ModifierKey, action glfw.Action) {
	s := a.search
	if action == glfw.Release {
		return
	}
	shift := mods&glfw.ModShift != 0
	ctrl := mods&glfw.ModControl != 0
	switch {
	case key == glfw.KeyEscape:
		a.closeSearch()
		return
	case key == glfw.KeyEnter || key == glfw.KeyKPEnter:
		a.searchStep(shift) // Enter: older; Shift+Enter: newer
	case key == glfw.KeyUp:
		a.searchStep(false)
	case key == glfw.KeyDown:
		a.searchStep(true)
	case key == glfw.KeyBackspace && len(s.query) > 0:
		if ctrl {
			s.query = s.query[:0]
		} else {
			s.query = s.query[:len(s.query)-1]
		}
		a.runSearch()
	case ctrl && key == glfw.KeyU:
		s.query = s.query[:0]
		a.runSearch()
	case ctrl && key == glfw.KeyV:
		a.searchType(glfw.GetClipboardString())
	case ctrl && shift && key == glfw.KeyF:
		a.searchStep(false) // pressing the shortcut again finds the next older match
	}
}

// searchType adds typed (or pasted) text to the query.
func (a *App) searchType(text string) {
	s := a.search
	for _, r := range text {
		if r == '\n' || r == '\r' {
			break
		}
		if unicode.IsPrint(r) {
			s.query = append(s.query, r)
		}
	}
	a.runSearch()
}

// runSearch finds all matches for the query and jumps to the newest.
func (a *App) runSearch() {
	s := a.search
	s.gen++
	a.forceRedraw = true
	s.matches, s.cur, s.capped = nil, -1, false
	if len(s.query) == 0 {
		return
	}
	s.matches, s.capped = findAll(s.pane.term, string(s.query))
	if len(s.matches) > 0 {
		s.cur = len(s.matches) - 1
		a.revealMatch()
	}
}

// searchStep moves to the next older (newer=false) or newer match.
func (a *App) searchStep(newer bool) {
	s := a.search
	if len(s.matches) == 0 {
		return
	}
	if newer {
		s.cur = (s.cur + 1) % len(s.matches)
	} else {
		s.cur = (s.cur - 1 + len(s.matches)) % len(s.matches)
	}
	s.gen++
	a.revealMatch()
	a.forceRedraw = true
}

// revealMatch scrolls so the current match is on screen (above the bar).
func (a *App) revealMatch() {
	s := a.search
	p := s.pane
	m := s.matches[s.cur]
	p.term.Lock()
	_, rows := p.term.Size()
	sb := p.term.ScrollbackLen()
	top := p.term.AbsRow(0, p.offset)
	p.term.Unlock()
	bottom := top + int64(rows) - 2 // the bar covers the last row
	if m.startRow >= top && m.endRow <= bottom {
		return
	}
	// Put the match in the middle: AbsRow(y, off) = AbsRow(0, 0) - off + y.
	live := top + int64(p.offset)
	off := int(live + int64(rows/2) - m.startRow)
	p.offset = max(0, min(off, sb))
}

// findAll searches a terminal's scrollback and screen. Soft-wrapped lines
// are searched as one, so a match can cross the wrap.
func findAll(t *term.Terminal, query string) ([]searchMatch, bool) {
	fold := strings.ToLower(query) == query // smart case
	if fold {
		query = strings.ToLower(query)
	}
	t.Lock()
	defer t.Unlock()
	_, rows := t.Size()
	top := t.Pushed() - int64(t.ScrollbackLen())
	bottom := t.Pushed() + int64(rows) - 1
	var out []searchMatch
	for r := top; r <= bottom; {
		text, at := logicalLine(t, r)
		next := r + 1
		if len(at) > 0 {
			next = at[len(at)-1].row + 1
		}
		hay := text
		if fold {
			hay = foldASCIIPreservingLength(text)
		}
		for i := 0; ; {
			j := strings.Index(hay[i:], query)
			if j < 0 {
				break
			}
			start, end := i+j, i+j+len(query)-1
			if end < len(at) {
				out = append(out, searchMatch{at[start].row, at[start].col, at[end].row, at[end].col})
				if len(out) >= maxMatches {
					return out, true
				}
			}
			i = start + max(1, len(query))
		}
		r = next
	}
	return out, false
}

// foldASCIIPreservingLength lowercases text without changing byte offsets
// (strings.ToLower can change the length of some non-ASCII text, which
// would break the byte-to-cell mapping): non-ASCII runes are lowered only
// when their encoded length stays the same.
func foldASCIIPreservingLength(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		l := unicode.ToLower(r)
		if len(string(l)) != len(string(r)) {
			l = r
		}
		b.WriteRune(l)
	}
	return b.String()
}

// searchView adds the search highlights and bar to a pane's view.
func (a *App) searchView(p *pane, v *render.View) {
	s := a.search
	if s == nil || s.pane != p {
		return
	}
	v.OverlayGen, v.HighlightGen = s.gen, s.gen
	p.term.Lock()
	cols, rows := p.term.Size()
	top := p.term.AbsRow(0, p.offset)
	p.term.Unlock()
	bottom := top + int64(rows) - 1
	for i, m := range s.matches {
		if m.endRow < top || m.startRow > bottom {
			continue
		}
		for row := m.startRow; row <= m.endRow; row++ {
			h := render.Highlight{Row: row, StartCol: 0, EndCol: cols - 1, Current: i == s.cur}
			if row == m.startRow {
				h.StartCol = m.startCol
			}
			if row == m.endRow {
				h.EndCol = m.endCol
			}
			v.Highlights = append(v.Highlights, h)
		}
	}
	v.Overlay = a.searchBar(cols)
}

// searchBar draws the search bar as a terminal line.
func (a *App) searchBar(cols int) *term.Line {
	s := a.search
	th := s.pane.surf.Theme()
	rgb := func(c [3]uint8) term.Color { return term.RGBColor(c[0], c[1], c[2]) }
	bg := blendRGB(th.Background, th.Foreground, 0.12)
	cells := make([]term.Cell, cols)
	for i := range cells {
		cells[i] = term.Cell{BG: rgb([3]uint8{bg.R, bg.G, bg.B})}
	}
	x := 0
	put := func(text string, fg term.Color, attrs term.Attr) {
		for _, r := range text {
			if x < cols {
				cells[x] = term.Cell{Rune: r, FG: fg, BG: cells[x].BG, Attrs: attrs}
				x++
			}
		}
	}
	accent := rgb([3]uint8{th.Palette[4].R, th.Palette[4].G, th.Palette[4].B})
	fgc := rgb([3]uint8{th.Foreground.R, th.Foreground.G, th.Foreground.B})
	dim := rgb([3]uint8{th.Palette[8].R, th.Palette[8].G, th.Palette[8].B})
	put(" search ", accent, term.AttrBold)
	put(string(s.query), fgc, 0)
	put("▏", accent, 0)

	var info string
	switch {
	case len(s.query) == 0:
		info = "type to search · Esc closes "
	case len(s.matches) == 0:
		info = "no matches · Esc closes "
	default:
		more := ""
		if s.capped {
			more = "+"
		}
		info = fmt.Sprintf("%d/%d%s · Enter older · Shift+Enter newer · Esc ", s.cur+1, len(s.matches), more)
	}
	if start := cols - len([]rune(info)); start > x+1 {
		x = start
		put(info, dim, 0)
	}
	return term.NewLine(cells)
}

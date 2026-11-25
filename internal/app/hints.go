package app

import (
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/go-gl/glfw/v3.3/glfw"

	"github.com/zach-monroe/germ/internal/render"
	"github.com/zach-monroe/germ/internal/term"
)

// Keyboard hints (Ctrl+Shift+E): every URL, file reference, git hash and
// IP address on the active pane's screen gets a letter. Typing it opens the
// thing (URLs and host:port dev servers in the browser, files in $EDITOR at
// the line) or copies it (hashes, bare addresses); Shift+letter always
// copies. Esc cancels.

type hintKind uint8

const (
	hintURL hintKind = iota
	hintFile
	hintHash
	hintIP
)

type hint struct {
	kind  hintKind
	text  string
	file  *fileRef
	label string
	start cellPos
	end   cellPos // inclusive
}

type hintState struct {
	pane  *pane
	hints []hint
	typed string
	gen   int
}

var (
	hashRE = regexp.MustCompile(`\b[0-9a-f]{7,40}\b`)
	ipRE   = regexp.MustCompile(`\b(?:(?:\d{1,3}\.){3}\d{1,3}(?::\d{1,5})?|localhost:\d{1,5})\b`)
)

// hintAlphabet puts the easiest keys first.
const hintAlphabet = "asdfjklghqwertyuiopzxcvbnm"

// openHints labels the active pane's screen; false if there's nothing.
func (a *App) openHints() bool {
	p := a.active()
	if p == nil {
		return false
	}
	a.closeSearch()
	dir := ""
	_, widget := p.be.(*widgetBackend)
	if !widget && (a.tmux == nil || a.tmux.remote == nil) { // remote paths aren't ours
		dir = paneDir(p)
	}
	hs := findHints(p.term, p.offset, dir)
	if len(hs) == 0 {
		a.setStatus("no links, files or hashes on screen", 2*time.Second)
		return true
	}
	labelHints(hs)
	a.hints = &hintState{pane: p, hints: hs, gen: 1}
	a.forceRedraw = true
	return true
}

func (a *App) closeHints() {
	if a.hints != nil {
		a.hints = nil
		a.forceRedraw = true
	}
}

// labelHints gives each hint a label: one letter while they fit, else two
// letters for all (so no label is a prefix of another). The bottom of the
// screen, where the newest output is, gets the easiest keys.
func labelHints(hs []hint) {
	n := len(hs)
	k := len(hintAlphabet)
	for i := range hs {
		j := n - 1 - i // newest first
		if n <= k {
			hs[i].label = string(hintAlphabet[j])
		} else {
			hs[i].label = string(hintAlphabet[j/k%k]) + string(hintAlphabet[j%k])
		}
	}
}

// findHints scans the visible rows. Caller must not hold the lock.
func findHints(t *term.Terminal, offset int, dir string) []hint {
	t.Lock()
	defer t.Unlock()
	_, rows := t.Size()
	top := t.AbsRow(0, offset)
	bottom := top + int64(rows) - 1
	var out []hint
	taken := func(s, e cellPos) bool {
		for _, h := range out {
			if !(cellLess(e, h.start) || cellLess(h.end, s)) {
				return true
			}
		}
		return false
	}
	add := func(h hint) {
		if h.end.row < top || h.start.row > bottom || taken(h.start, h.end) || len(out) >= 26*26 {
			return
		}
		out = append(out, h)
	}
	for r := top; r <= bottom; {
		// OSC 8 hyperlinks on this row.
		if l := t.LineAt(r); l != nil {
			for x := 0; x < len(l.Cells); x++ {
				id := l.Cells[x].Link
				if id == 0 {
					continue
				}
				e := x
				for e+1 < len(l.Cells) && l.Cells[e+1].Link == id {
					e++
				}
				if u := t.LinkURL(id); u != "" {
					add(hint{kind: hintURL, text: u, start: cellPos{r, x}, end: cellPos{r, e}})
				}
				x = e
			}
		}
		text, at := logicalLine(t, r)
		next := r + 1
		if len(at) > 0 {
			next = at[len(at)-1].row + 1
		}
		span := func(s, e int) (cellPos, cellPos) { return at[s], at[e-1] }
		for _, m := range urlMatches(text) {
			s, e := span(m[0], m[1])
			add(hint{kind: hintURL, text: text[m[0]:m[1]], start: s, end: e})
		}
		if dir != "" || strings.Contains(text, "/") {
			for _, m := range fileRE.FindAllStringSubmatchIndex(text, -1) {
				if m[1]-m[0] < 3 {
					continue
				}
				if ref := resolveFile(text, m, dir); ref != nil {
					s, e := span(m[0], m[1])
					add(hint{kind: hintFile, text: strings.TrimRight(text[m[0]:m[1]], "."), file: ref, start: s, end: e})
				}
			}
		}
		for _, m := range hashRE.FindAllStringIndex(text, -1) {
			h := text[m[0]:m[1]]
			if strings.IndexFunc(h, unicode.IsLetter) < 0 || strings.IndexFunc(h, unicode.IsDigit) < 0 {
				continue // a word or a number
			}
			s, e := span(m[0], m[1])
			add(hint{kind: hintHash, text: h, start: s, end: e})
		}
		for _, m := range ipRE.FindAllStringIndex(text, -1) {
			s, e := span(m[0], m[1])
			add(hint{kind: hintIP, text: text[m[0]:m[1]], start: s, end: e})
		}
		r = next
	}
	sortHints(out)
	return out
}

func cellLess(a, b cellPos) bool { return a.row < b.row || a.row == b.row && a.col < b.col }

// sortHints orders hints top to bottom, left to right.
func sortHints(hs []hint) {
	for i := 1; i < len(hs); i++ {
		for j := i; j > 0 && cellLess(hs[j].start, hs[j-1].start); j-- {
			hs[j], hs[j-1] = hs[j-1], hs[j]
		}
	}
}

// hintsKey handles keys while hints are shown; it consumes every key.
func (a *App) hintsKey(key glfw.Key, action glfw.Action) {
	if action == glfw.Release {
		return
	}
	switch key {
	case glfw.KeyEscape:
		a.closeHints()
	case glfw.KeyBackspace:
		if h := a.hints; h.typed != "" {
			h.typed = h.typed[:len(h.typed)-1]
			h.gen++
			a.forceRedraw = true
		} else {
			a.closeHints()
		}
	}
}

// hintsType handles a typed letter: lowercase opens, uppercase copies.
func (a *App) hintsType(r rune) {
	h := a.hints
	lower := unicode.ToLower(r)
	if !strings.ContainsRune(hintAlphabet, lower) {
		return
	}
	h.typed += string(lower)
	h.gen++
	a.forceRedraw = true
	var match *hint
	found := false
	for i := range h.hints {
		if strings.HasPrefix(h.hints[i].label, h.typed) {
			found = true
			if h.hints[i].label == h.typed {
				match = &h.hints[i]
			}
		}
	}
	if !found {
		h.typed = h.typed[:len(h.typed)-1] // not a label: ignore it
		return
	}
	if match == nil {
		return // first letter of a two-letter label
	}
	p := h.pane
	a.closeHints()
	a.useHint(p, *match, unicode.IsUpper(r))
}

// useHint opens or copies a chosen hint.
func (a *App) useHint(p *pane, h hint, copyIt bool) {
	if !copyIt {
		switch h.kind {
		case hintURL:
			openURL(h.text)
			return
		case hintFile:
			a.openInEditor(p, h.file)
			return
		case hintIP:
			if strings.Contains(h.text, ":") { // a dev server
				openURL("http://" + h.text)
				return
			}
		}
	}
	glfw.SetClipboardString(h.text)
	setPrimary(h.text)
	shown := h.text
	if r := []rune(shown); len(r) > 50 {
		shown = string(r[:49]) + "…"
	}
	a.setStatus("copied "+shown, 2*time.Second)
}

// hintsView adds the hints to a pane's view.
func (a *App) hintsView(p *pane, v *render.View) {
	h := a.hints
	if h == nil || h.pane != p {
		return
	}
	v.HighlightGen += 1 << 20 * h.gen // distinct from search generations
	cols, _ := p.term.Size()
	for _, x := range h.hints {
		if !strings.HasPrefix(x.label, h.typed) {
			continue
		}
		label := x.label[len(h.typed):]
		for row := x.start.row; row <= x.end.row; row++ {
			hl := render.Highlight{Row: row, StartCol: 0, EndCol: cols - 1}
			if row == x.start.row {
				hl.StartCol, hl.Label = x.start.col, label
			}
			if row == x.end.row {
				hl.EndCol = x.end.col
			}
			v.Highlights = append(v.Highlights, hl)
		}
	}
	v.Overlay = a.hintsBar(p, cols)
	v.OverlayGen += 1 << 20 * h.gen
}

// hintsBar is the bottom-row help line.
func (a *App) hintsBar(p *pane, cols int) *term.Line {
	th := p.surf.Theme()
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
	dim := rgb([3]uint8{th.Palette[8].R, th.Palette[8].G, th.Palette[8].B})
	put(" hints ", accent, term.AttrBold)
	put(fmt.Sprintf("%d · letter opens · Shift+letter copies · Esc cancels", len(a.hints.hints)), dim, 0)
	return term.NewLine(cells)
}

package app

import (
	"os/exec"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-gl/glfw/v3.3/glfw"

	"github.com/zach-monroe/germ/internal/input"
	"github.com/zach-monroe/germ/internal/render"
	"github.com/zach-monroe/germ/internal/term"
)

const (
	multiClickInterval = 400 * time.Millisecond
	wheelLines         = 3
)

type mouseState struct {
	px, py     float64 // last position, framebuffer pixels
	col, row   int     // last cell (viewport coordinates)
	pressed    input.Button
	anyPressed bool
	lastClick  time.Time
	clickCol   int
	clickRow   int64
	clicks     int
}

// ---- selection -----------------------------------------------------------

type selMode uint8

const (
	selChar selMode = iota
	selWord
	selLine
)

type selPoint struct {
	row int64
	col int
}

func (p selPoint) before(q selPoint) bool {
	return p.row < q.row || (p.row == q.row && p.col < q.col)
}

type selection struct {
	active   bool // something is selected
	dragging bool
	mode     selMode
	anchor   selPoint
	head     selPoint
}

func (s *selection) clear() { *s = selection{} }

// bounds returns the normalized, mode-expanded selection. Caller must
// hold the terminal lock.
func (s *selection) bounds(t *term.Terminal) (start, end selPoint) {
	start, end = s.anchor, s.head
	if end.before(start) {
		start, end = end, start
	}
	switch s.mode {
	case selWord:
		start.col = wordEdge(t.LineAt(start.row), start.col, -1)
		end.col = wordEdge(t.LineAt(end.row), end.col, 1)
	case selLine:
		// Extend over soft-wrapped continuation lines.
		for l := t.LineAt(start.row - 1); l != nil && l.Wrapped; l = t.LineAt(start.row - 1) {
			start.row--
		}
		for l := t.LineAt(end.row); l != nil && l.Wrapped; l = t.LineAt(end.row) {
			end.row++
		}
		cols, _ := t.Size()
		start.col, end.col = 0, cols-1
	}
	return start, end
}

func (s *selection) render(t *term.Terminal) render.Selection {
	if !s.active {
		return render.Selection{}
	}
	t.Lock()
	defer t.Unlock()
	start, end := s.bounds(t)
	return render.Selection{Active: true, StartRow: start.row, StartCol: start.col, EndRow: end.row, EndCol: end.col}
}

func (s *selection) text(t *term.Terminal) string {
	if !s.active {
		return ""
	}
	t.Lock()
	defer t.Unlock()
	start, end := s.bounds(t)
	return t.TextRange(start.row, start.col, end.row, end.col)
}

// isWordRune reports whether r is part of a word for double-click
// selection. Path and URL punctuation counts, so a double-click selects
// a whole path or URL.
func isWordRune(r rune) bool {
	if r == 0 || r == ' ' || r == '\t' {
		return false
	}
	return !strings.ContainsRune("\"'`()[]{}<>|│,;", r)
}

// wordEdge scans from col in direction dir (-1 or 1) to the edge of the
// word containing col.
func wordEdge(l *term.Line, col, dir int) int {
	if l == nil || col < 0 || col >= len(l.Cells) {
		return col
	}
	runeAt := func(x int) rune {
		c := l.Cells[x]
		if c.Attrs&term.AttrWideSpacer != 0 && x > 0 {
			return l.Cells[x-1].Rune
		}
		return c.Rune
	}
	word := isWordRune(runeAt(col))
	for {
		next := col + dir
		if next < 0 || next >= len(l.Cells) || isWordRune(runeAt(next)) != word {
			return col
		}
		if !word && runeAt(next) != runeAt(col) {
			return col // runs of blanks/punctuation only extend over the same rune
		}
		col = next
	}
}

// ---- coordinates ------------------------------------------------------------

func (a *App) fbRatio() float64 {
	ww, _ := a.win.GetSize()
	fw, _ := a.win.GetFramebufferSize()
	return float64(fw) / float64(max(ww, 1))
}

// cellAt converts framebuffer pixels to a viewport cell, clamped to the grid.
func (a *App) cellAt(px, py float64) (col, row int) {
	cw, ch := a.render.CellSize()
	pad := float64(a.render.Pad())
	a.term.Lock()
	cols, rows := a.term.Size()
	a.term.Unlock()
	col = max(0, min(int((px-pad)/float64(cw)), cols-1))
	row = max(0, min(int((py-pad)/float64(ch)), rows-1))
	return col, row
}

func (a *App) absPoint(col, row int) selPoint {
	a.term.Lock()
	defer a.term.Unlock()
	return selPoint{row: a.term.AbsRow(row, a.offset), col: col}
}

// reporting reports whether mouse events go to the application. Holding
// Shift always gives the mouse back to germ for selection.
func (a *App) reporting(mods glfw.ModifierKey) (term.MouseMode, term.MouseEncoding, bool) {
	a.term.Lock()
	m := a.term.Modes()
	a.term.Unlock()
	return m.Mouse, m.MouseEncoding, m.Mouse != term.MouseNone && mods&glfw.ModShift == 0
}

func (a *App) reportMouse(btn input.Button, act input.MouseAction, mods glfw.ModifierKey) {
	mode, enc, ok := a.reporting(mods)
	if !ok {
		return
	}
	pad := float64(a.render.Pad())
	a.send(input.EncodeMouse(input.MouseEvent{
		Button: btn, Action: act, X: a.mouse.col, Y: a.mouse.row,
		PX: max(0, int(a.mouse.px-pad)), PY: max(0, int(a.mouse.py-pad)),
		Mods: toMods(mods) &^ input.ModSuper,
	}, mode, enc))
}

// ---- callbacks ----------------------------------------------------------------

func toButton(b glfw.MouseButton) (input.Button, bool) {
	switch b {
	case glfw.MouseButtonLeft:
		return input.ButtonLeft, true
	case glfw.MouseButtonMiddle:
		return input.ButtonMiddle, true
	case glfw.MouseButtonRight:
		return input.ButtonRight, true
	}
	return 0, false
}

func (a *App) onMouseButton(_ *glfw.Window, b glfw.MouseButton, action glfw.Action, mods glfw.ModifierKey) {
	btn, ok := toButton(b)
	if !ok {
		return
	}
	if _, _, rep := a.reporting(mods); rep {
		act := input.MousePress
		if action == glfw.Release {
			act = input.MouseRelease
		}
		a.mouse.pressed, a.mouse.anyPressed = btn, action == glfw.Press
		a.reportMouse(btn, act, mods)
		return
	}

	switch {
	case btn == input.ButtonLeft && action == glfw.Press:
		if mods&glfw.ModControl != 0 && a.linkURL != "" {
			openURL(a.linkURL)
			return
		}
		p := a.absPoint(a.mouse.col, a.mouse.row)
		now := time.Now()
		if now.Sub(a.mouse.lastClick) < multiClickInterval && p.row == a.mouse.clickRow && p.col == a.mouse.clickCol {
			a.mouse.clicks = a.mouse.clicks%3 + 1
		} else {
			a.mouse.clicks = 1
		}
		a.mouse.lastClick, a.mouse.clickRow, a.mouse.clickCol = now, p.row, p.col
		a.sel = selection{
			active:   a.mouse.clicks > 1,
			dragging: true,
			mode:     selMode(a.mouse.clicks - 1),
			anchor:   p,
			head:     p,
		}
		a.forceRedraw = true
		if a.sel.active {
			setPrimary(a.sel.text(a.term))
		}
	case btn == input.ButtonLeft && action == glfw.Release:
		if a.sel.dragging && a.sel.active {
			setPrimary(a.sel.text(a.term))
		}
		a.sel.dragging = false
	case btn == input.ButtonMiddle && action == glfw.Press:
		a.pastePrimary()
	}
}

func (a *App) onCursorPos(_ *glfw.Window, x, y float64) {
	r := a.fbRatio()
	a.mouse.px, a.mouse.py = x*r, y*r
	col, row := a.cellAt(a.mouse.px, a.mouse.py)
	moved := col != a.mouse.col || row != a.mouse.row
	a.mouse.col, a.mouse.row = col, row

	if mode, _, rep := a.reporting(glfwMods(a.win)); rep {
		if moved && (mode == term.MouseAny || (mode == term.MouseButton && a.mouse.anyPressed)) {
			btn := input.ButtonNone
			if a.mouse.anyPressed {
				btn = a.mouse.pressed
			}
			a.reportMouse(btn, input.MouseMotion, glfwMods(a.win))
		}
		return
	}

	if a.sel.dragging && moved {
		a.sel.head = a.absPoint(col, row)
		a.sel.active = true
		a.forceRedraw = true
	}
	if moved {
		a.updateLinkHover()
	}
}

func (a *App) onScroll(_ *glfw.Window, _, yoff float64) {
	a.scrollAcc += yoff
	notches := int(a.scrollAcc)
	if notches == 0 {
		return
	}
	a.scrollAcc -= float64(notches)
	mods := glfwMods(a.win)

	if _, _, rep := a.reporting(mods); rep {
		btn := input.ButtonWheelUp
		if notches < 0 {
			btn = input.ButtonWheelDown
		}
		for range abs(notches) {
			a.reportMouse(btn, input.MousePress, mods)
		}
		return
	}

	a.term.Lock()
	alt, m := a.term.AltScreen(), a.term.Modes()
	a.term.Unlock()
	if alt {
		if m.AltScroll { // the wheel moves the cursor in pagers/editors
			key := input.KeyUp
			if notches < 0 {
				key = input.KeyDown
			}
			st := input.State{AppCursor: m.AppCursor}
			for range abs(notches) * wheelLines {
				a.send(input.EncodeKey(input.KeyEvent{Key: key, Action: input.Press}, st))
			}
		}
		return
	}
	a.scrollBy(notches * wheelLines)
}

func glfwMods(w *glfw.Window) glfw.ModifierKey {
	var m glfw.ModifierKey
	if w.GetKey(glfw.KeyLeftShift) == glfw.Press || w.GetKey(glfw.KeyRightShift) == glfw.Press {
		m |= glfw.ModShift
	}
	if w.GetKey(glfw.KeyLeftControl) == glfw.Press || w.GetKey(glfw.KeyRightControl) == glfw.Press {
		m |= glfw.ModControl
	}
	if w.GetKey(glfw.KeyLeftAlt) == glfw.Press || w.GetKey(glfw.KeyRightAlt) == glfw.Press {
		m |= glfw.ModAlt
	}
	return m
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// ---- hyperlinks ------------------------------------------------------------

var urlRE = regexp.MustCompile(`(?:https?|file|ftp)://[^\s<>"'` + "`" + `]+|mailto:[^\s<>"']+`)

// updateLinkHover underlines the URL under the pointer while Ctrl is held.
func (a *App) updateLinkHover() {
	var link render.Link
	url := ""
	if glfwMods(a.win)&glfw.ModControl != 0 {
		a.term.Lock()
		row := a.term.AbsRow(a.mouse.row, a.offset)
		url, link = findURL(a.term, row, a.mouse.col)
		a.term.Unlock()
	}
	if link != a.link {
		a.link, a.linkURL = link, url
		a.forceRedraw = true
	}
	a.linkURL = url
}

// findURL finds a URL covering (row, col), searching the whole logical
// (soft-wrapped) line. The returned highlight covers the part on row.
// Caller must hold the terminal lock.
func findURL(t *term.Terminal, row int64, col int) (string, render.Link) {
	first := row
	for l := t.LineAt(first - 1); l != nil && l.Wrapped; l = t.LineAt(first - 1) {
		first--
	}
	type pos struct {
		row int64
		col int
	}
	var text strings.Builder
	var at []pos // byte offset -> cell
	for r := first; ; r++ {
		l := t.LineAt(r)
		if l == nil {
			break
		}
		for x, c := range l.Cells {
			if c.Attrs&term.AttrWideSpacer != 0 {
				continue
			}
			ch := c.Rune
			if ch == 0 {
				ch = ' '
			}
			n := utf8.RuneLen(ch)
			for range n {
				at = append(at, pos{r, x})
			}
			text.WriteRune(ch)
		}
		if !l.Wrapped {
			break
		}
	}
	s := text.String()
	for _, m := range urlRE.FindAllStringIndex(s, -1) {
		start, end := m[0], m[1]
		// Trim trailing punctuation that's usually sentence, not URL.
		for end > start && strings.ContainsRune(".,;:!?)]}'\"", rune(s[end-1])) {
			if s[end-1] == ')' && strings.Count(s[start:end], "(") >= strings.Count(s[start:end], ")") {
				break // keep balanced parens, e.g. Wikipedia URLs
			}
			end--
		}
		if end <= start {
			continue
		}
		p0, p1 := at[start], at[end-1]
		if (row > p0.row || (row == p0.row && col >= p0.col)) && (row < p1.row || (row == p1.row && col <= p1.col)) {
			link := render.Link{Active: true, Row: row, StartCol: 0, EndCol: 1 << 30}
			if row == p0.row {
				link.StartCol = p0.col
			}
			if row == p1.row {
				link.EndCol = p1.col
			}
			return s[start:end], link
		}
	}
	return "", render.Link{}
}

func openURL(url string) {
	cmd := exec.Command("xdg-open", url)
	if err := cmd.Start(); err == nil {
		go cmd.Wait()
	}
}

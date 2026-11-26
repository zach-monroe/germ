package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-gl/glfw/v3.3/glfw"

	"github.com/zach-monroe/germ/internal/control"
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
	gx, gy     int     // last window grid cell
	capture    *pane   // pane that received the press, until release
	pressed    input.Button
	anyPressed bool
	lastClick  time.Time
	clickCol   int
	clickRow   int64
	clicks     int
	sep        *sepDrag // separator being dragged
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

// gridAt converts framebuffer pixels to a window grid cell, clamped.
func (a *App) gridAt(px, py float64) (gx, gy int) {
	cw, ch := a.render.CellSize()
	pad := float64(a.render.Pad())
	gx = max(0, min(int((px-pad)/float64(cw)), a.gridCols-1))
	gy = max(0, min(int((py-pad)/float64(ch)), a.gridRows-1))
	return gx, gy
}

// hit finds what's under the pointer: the tab bar, or a pane and the cell
// within it. While dragging, the pane that got the press keeps the mouse
// and coordinates are clamped to it.
func (a *App) hit() (p *pane, col, row int, inBar bool) {
	gx, gy := a.mouse.gx, a.mouse.gy
	bar := a.barRows()
	if a.mouse.capture == nil && gy < bar {
		return nil, gx, gy, true
	}
	gy -= bar
	t := a.tab()
	if t == nil {
		return nil, 0, 0, false
	}
	if c := a.mouse.capture; c != nil {
		r := c.rect
		return c, max(0, min(gx-r.X, r.W-1)), max(0, min(gy-r.Y, r.H-1)), false
	}
	for _, q := range t.visible() {
		if q.rect.contains(gx, gy) {
			return q, gx - q.rect.X, gy - q.rect.Y, false
		}
	}
	return nil, 0, 0, false
}

func absPoint(p *pane, col, row int) selPoint {
	p.term.Lock()
	defer p.term.Unlock()
	return selPoint{row: p.term.AbsRow(row, p.offset), col: col}
}

// reporting reports whether mouse events go to the pane's application.
// Holding Shift always gives the mouse back to germ for selection.
func reporting(p *pane, mods glfw.ModifierKey) (term.MouseMode, term.MouseEncoding, bool) {
	p.term.Lock()
	m := p.term.Modes()
	p.term.Unlock()
	return m.Mouse, m.MouseEncoding, m.Mouse != term.MouseNone && mods&glfw.ModShift == 0
}

func (a *App) reportMouse(p *pane, col, row int, btn input.Button, act input.MouseAction, mods glfw.ModifierKey) {
	mode, enc, ok := reporting(p, mods)
	if !ok || a.helmBlocked(p) != "" {
		return
	}
	ox, oy, _ := a.paneOrigin(p)
	p.send(input.EncodeMouse(input.MouseEvent{
		Button: btn, Action: act, X: col, Y: row,
		PX: max(0, int(a.mouse.px)-ox), PY: max(0, int(a.mouse.py)-oy),
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
	if action == glfw.Press {
		// Ask where the pointer is: if it hasn't moved since the window
		// appeared under it, no motion event has told us.
		x, y := a.win.GetCursorPos()
		r := a.fbRatio()
		a.mouse.px, a.mouse.py = x*r, y*r
		a.mouse.gx, a.mouse.gy = a.gridAt(a.mouse.px, a.mouse.py)
	}
	if a.mouse.sep != nil && action == glfw.Release {
		a.endSepDrag()
		return
	}
	if action == glfw.Press && btn == input.ButtonLeft && a.startSepDrag(a.mouse.gx, a.mouse.gy) {
		return
	}
	if action == glfw.Press && a.palette != nil && a.menuClick() {
		return
	}
	if action == glfw.Press && btn == input.ButtonRight && a.mouse.capture == nil && a.rightClick(mods) {
		return
	}
	if row, ok := a.chatHit(a.mouse.gx, a.mouse.gy); ok && a.mouse.capture == nil {
		if action == glfw.Press && btn == input.ButtonLeft {
			a.chatClick(row)
		}
		return
	}
	if action == glfw.Press && a.chat.focused {
		a.chat.focused, a.chat.dirty, a.forceRedraw = false, true, true // back to the terminal
	}
	p, col, row, inBar := a.hit()
	if action == glfw.Release {
		defer func() { a.mouse.capture = nil }()
	}
	if inBar {
		if action == glfw.Press && (btn == input.ButtonLeft || btn == input.ButtonMiddle) {
			for _, s := range a.tabSpans {
				if col >= s.start && col < s.end {
					a.switchTab(s.index)
					// Middle-click, or the ×, closes it.
					if btn == input.ButtonMiddle || (s.close >= 0 && col >= s.close && col < s.close+2) {
						a.actionCloseTab()
					}
				}
			}
		}
		return
	}
	if p == nil {
		return
	}
	if action == glfw.Press {
		a.mouse.capture = p
		if t := a.tab(); t != nil && p != t.active {
			a.setActive(t, p) // click to focus
		}
	}

	if _, _, rep := reporting(p, mods); rep {
		act := input.MousePress
		if action == glfw.Release {
			act = input.MouseRelease
		}
		a.mouse.pressed, a.mouse.anyPressed = btn, action == glfw.Press
		a.reportMouse(p, col, row, btn, act, mods)
		return
	}

	switch {
	case btn == input.ButtonLeft && action == glfw.Press:
		// What's under the pointer now: the hover state is from the last
		// motion, and the screen may have changed since (a widget redraws,
		// the agents list reorders, or you came back to this tab).
		a.updateLinkHover()
		_, widget := p.be.(*widgetBackend)
		if (widget || mods&glfw.ModControl != 0) && p.linkURL != "" {
			openURL(p.linkURL)
			return
		}
		if mods&glfw.ModControl != 0 && p.fileRef != nil {
			a.openInEditor(p, p.fileRef)
			return
		}
		pt := absPoint(p, col, row)
		now := time.Now()
		if now.Sub(a.mouse.lastClick) < multiClickInterval && pt.row == a.mouse.clickRow && pt.col == a.mouse.clickCol {
			a.mouse.clicks = a.mouse.clicks%3 + 1
		} else {
			a.mouse.clicks = 1
		}
		a.mouse.lastClick, a.mouse.clickRow, a.mouse.clickCol = now, pt.row, pt.col
		for _, q := range a.panes {
			if q != p && q.sel.active {
				q.sel.clear() // one selection at a time
			}
		}
		p.sel = selection{
			active:   a.mouse.clicks > 1,
			dragging: true,
			mode:     selMode(a.mouse.clicks - 1),
			anchor:   pt,
			head:     pt,
		}
		a.forceRedraw = true
		if p.sel.active {
			setPrimary(p.sel.text(p.term))
		}
	case btn == input.ButtonLeft && action == glfw.Release:
		if p.sel.dragging && p.sel.active {
			setPrimary(p.sel.text(p.term))
		}
		p.sel.dragging = false
	case btn == input.ButtonMiddle && action == glfw.Press:
		a.pastePrimary()
	}
}

func (a *App) onCursorPos(_ *glfw.Window, x, y float64) {
	r := a.fbRatio()
	a.mouse.px, a.mouse.py = x*r, y*r
	gx, gy := a.gridAt(a.mouse.px, a.mouse.py)
	moved := gx != a.mouse.gx || gy != a.mouse.gy
	a.mouse.gx, a.mouse.gy = gx, gy
	if !moved {
		return
	}
	if a.mouse.sep != nil {
		a.dragSep(gx, gy)
		return
	}
	p, col, row, inBar := a.hit()
	if inBar || p == nil {
		return
	}

	if mode, _, rep := reporting(p, glfwMods(a.win)); rep {
		if mode == term.MouseAny || (mode == term.MouseButton && a.mouse.anyPressed) {
			btn := input.ButtonNone
			if a.mouse.anyPressed {
				btn = a.mouse.pressed
			}
			a.reportMouse(p, col, row, btn, input.MouseMotion, glfwMods(a.win))
		}
		return
	}

	if p.sel.dragging {
		p.sel.head = absPoint(p, col, row)
		p.sel.active = true
		a.forceRedraw = true
	}
	a.updateLinkHover()
}

func (a *App) onScroll(_ *glfw.Window, _, yoff float64) {
	if _, ok := a.chatHit(a.mouse.gx, a.mouse.gy); ok {
		if yoff > 0 {
			a.chatScroll(1)
		} else if yoff < 0 {
			a.chatScroll(-1)
		}
		return
	}
	p, col, row, inBar := a.hit()
	if inBar {
		if yoff > 0 {
			a.switchTab(a.cur - 1)
		} else if yoff < 0 {
			a.switchTab(a.cur + 1)
		}
		return
	}
	if p == nil {
		return
	}
	p.scrollAcc += yoff
	notches := int(p.scrollAcc)
	if notches == 0 {
		return
	}
	p.scrollAcc -= float64(notches)
	mods := glfwMods(a.win)

	if _, _, rep := reporting(p, mods); rep {
		btn := input.ButtonWheelUp
		if notches < 0 {
			btn = input.ButtonWheelDown
		}
		for range abs(notches) {
			a.reportMouse(p, col, row, btn, input.MousePress, mods)
		}
		return
	}

	p.term.Lock()
	alt, m, sb := p.term.AltScreen(), p.term.Modes(), p.term.ScrollbackLen()
	p.term.Unlock()
	if alt {
		if m.AltScroll { // the wheel moves the cursor in pagers/editors
			key := input.KeyUp
			if notches < 0 {
				key = input.KeyDown
			}
			st := input.State{AppCursor: m.AppCursor}
			for range abs(notches) * wheelLines {
				p.send(input.EncodeKey(input.KeyEvent{Key: key, Action: input.Press}, st))
			}
		}
		return
	}
	off := max(0, min(p.offset+notches*wheelLines, sb))
	if off != p.offset {
		p.offset = off
		a.forceRedraw = true
	}
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
	p, col, row, inBar := a.hit()
	for _, q := range a.panes {
		if q != p && q.link.Active {
			q.link, q.linkURL, q.fileRef = render.Link{}, "", nil
			a.forceRedraw = true
		}
	}
	if inBar || p == nil {
		return
	}
	var link render.Link
	url := ""
	_, widget := p.be.(*widgetBackend)
	if widget || glfwMods(a.win)&glfw.ModControl != 0 { // widgets don't take typing: no Ctrl needed
		p.term.Lock()
		abs := p.term.AbsRow(row, p.offset)
		url, link = findHyperlink(p.term, abs, col)
		if url == "" {
			url, link = findURL(p.term, abs, col)
		}
		p.term.Unlock()
		if url == "" && !widget && (a.tmux == nil || a.tmux.remote == nil) { // remote paths aren't ours
			dir := paneDir(p)
			p.term.Lock()
			ref, l := findFile(p.term, abs, col, dir)
			p.term.Unlock()
			if ref != nil {
				p.fileRef, link = ref, l
			}
		}
	}
	if link != p.link {
		p.link = link
		a.forceRedraw = true
	}
	p.linkURL = url
	a.setHandCursor(link.Active)
	if !link.Active || url != "" {
		p.fileRef = nil
	}
}

type cellPos struct {
	row int64
	col int
}

// logicalLine returns the text of the soft-wrapped line containing row and
// the cell position of each byte. Caller must hold the terminal lock.
func logicalLine(t *term.Terminal, row int64) (string, []cellPos) {
	first := row
	for l := t.LineAt(first - 1); l != nil && l.Wrapped; l = t.LineAt(first - 1) {
		first--
	}
	var text strings.Builder
	var at []cellPos // byte offset -> cell
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
				at = append(at, cellPos{r, x})
			}
			text.WriteRune(ch)
		}
		if !l.Wrapped {
			break
		}
	}
	return text.String(), at
}

// linkAt returns the highlight for the match [start, end) if it covers
// (row, col).
func linkAt(at []cellPos, start, end int, row int64, col int) (render.Link, bool) {
	p0, p1 := at[start], at[end-1]
	if !((row > p0.row || (row == p0.row && col >= p0.col)) && (row < p1.row || (row == p1.row && col <= p1.col))) {
		return render.Link{}, false
	}
	link := render.Link{Active: true, Row: row, StartCol: 0, EndCol: 1 << 30}
	if row == p0.row {
		link.StartCol = p0.col
	}
	if row == p1.row {
		link.EndCol = p1.col
	}
	return link, true
}

// findURL finds a URL covering (row, col), searching the whole logical
// (soft-wrapped) line. The returned highlight covers the part on row.
// Caller must hold the terminal lock.
func findURL(t *term.Terminal, row int64, col int) (string, render.Link) {
	s, at := logicalLine(t, row)
	for _, m := range urlMatches(s) {
		if link, ok := linkAt(at, m[0], m[1], row, col); ok {
			return s[m[0]:m[1]], link
		}
	}
	return "", render.Link{}
}

// urlMatches finds URLs in s, as byte ranges [start, end).
func urlMatches(s string) [][2]int {
	var out [][2]int
	for _, m := range urlRE.FindAllStringIndex(s, -1) {
		start, end := m[0], m[1]
		// Trim trailing punctuation that's usually sentence, not URL.
		for end > start && strings.ContainsRune(".,;:!?)]}'\"", rune(s[end-1])) {
			if s[end-1] == ')' && strings.Count(s[start:end], "(") >= strings.Count(s[start:end], ")") {
				break // keep balanced parens, e.g. Wikipedia URLs
			}
			end--
		}
		if end > start {
			out = append(out, [2]int{start, end})
		}
	}
	return out
}

// fileRE matches file references like internal/app/x.go:45:3, ./a.ts:12,
// or README.md, as printed by compilers, test runners, and agents.
var fileRE = regexp.MustCompile(`(?:~|\.{1,2})?/?(?:[\w@.+\-]+/)*[\w@+\-][\w@.+\-]*(?::(\d+)(?::(\d+))?)?`)

// fileRef is a clickable reference to a line in a file.
type fileRef struct {
	path      string
	line, col int
}

// findFile finds an existing file reference covering (row, col); relative
// paths resolve against dir. Caller must hold the terminal lock.
func findFile(t *term.Terminal, row int64, col int, dir string) (*fileRef, render.Link) {
	s, at := logicalLine(t, row)
	for _, m := range fileRE.FindAllStringSubmatchIndex(s, -1) {
		if m[1]-m[0] < 3 {
			continue
		}
		link, ok := linkAt(at, m[0], m[1], row, col)
		if !ok {
			continue
		}
		if ref := resolveFile(s, m, dir); ref != nil {
			return ref, link
		}
		return nil, render.Link{}
	}
	return nil, render.Link{}
}

// resolveFile turns a fileRE match in s into a reference to an existing
// file (relative paths resolve against dir), or nil.
func resolveFile(s string, m []int, dir string) *fileRef {
	pathEnd := m[1]
	if m[2] >= 0 {
		pathEnd = m[2] - 1 // before ":line"
	}
	path := strings.TrimRight(s[m[0]:pathEnd], ".")
	if !strings.ContainsAny(path, "/.") {
		return nil // a bare word, not a file
	}
	switch {
	case strings.HasPrefix(path, "~/"):
		home, _ := os.UserHomeDir()
		path = filepath.Join(home, path[2:])
	case !filepath.IsAbs(path):
		if dir == "" {
			return nil
		}
		path = filepath.Join(dir, path)
	}
	if fi, err := os.Stat(path); err != nil || fi.IsDir() {
		return nil
	}
	ref := &fileRef{path: path}
	if m[2] >= 0 {
		ref.line, _ = strconv.Atoi(s[m[2]:m[3]])
	}
	if m[4] >= 0 {
		ref.col, _ = strconv.Atoi(s[m[4]:m[5]])
	}
	return ref
}

// openURL opens a link with $BROWSER if set, else xdg-open. germ-pane:
// links focus a pane in a germ window instead.
func openURL(url string) {
	if ref, ok := strings.CutPrefix(url, "germ-pane:"); ok {
		go focusRemotePane(ref)
		return
	}
	argv := []string{"xdg-open"}
	if b := strings.Fields(os.Getenv("BROWSER")); len(b) > 0 {
		argv = b
	}
	cmd := exec.Command(argv[0], append(argv[1:], url)...)
	if err := cmd.Start(); err == nil {
		go cmd.Wait()
	}
}

// findHyperlink finds the OSC 8 link at (row, col) and the run of cells on
// that row it covers. Caller must hold the terminal lock.
func findHyperlink(t *term.Terminal, row int64, col int) (string, render.Link) {
	l := t.LineAt(row)
	if l == nil || col < 0 || col >= len(l.Cells) || l.Cells[col].Link == 0 {
		return "", render.Link{}
	}
	id := l.Cells[col].Link
	start, end := col, col
	for start > 0 && l.Cells[start-1].Link == id {
		start--
	}
	for end+1 < len(l.Cells) && l.Cells[end+1].Link == id {
		end++
	}
	return t.LinkURL(id), render.Link{Active: true, Row: row, StartCol: start, EndCol: end}
}

// setHandCursor shows a pointing hand over links.
func (a *App) setHandCursor(on bool) {
	if on == a.handShown {
		return
	}
	a.handShown = on
	if on {
		if a.handCursor == nil {
			a.handCursor = glfw.CreateStandardCursor(glfw.HandCursor)
		}
		a.win.SetCursor(a.handCursor)
	} else {
		a.win.SetCursor(nil)
	}
}

// focusRemotePane handles germ-pane:PID/PANE: focus that pane in the germ
// window with that process ID (possibly this one), via its control socket.
func focusRemotePane(ref string) {
	pid, pane, ok := strings.Cut(ref, "/")
	if !ok {
		return
	}
	if _, err := strconv.Atoi(pid); err != nil {
		return
	}
	c, err := control.Dial(control.SocketPath(atoi(pid)))
	if err != nil {
		return
	}
	defer c.Close()
	c.Call(control.Request{Cmd: control.CmdFocusPane, Pane: pane})
}

func atoi(s string) int { n, _ := strconv.Atoi(s); return n }

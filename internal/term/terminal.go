package term

import (
	"io"
	"strings"
	"sync"
	"time"

	"github.com/zach-monroe/germ/internal/theme"
	"github.com/zach-monroe/germ/internal/vt"
)

// Options configures a Terminal.
type Options struct {
	// Scrollback is the maximum number of scrollback lines (primary screen).
	Scrollback int
	// Responder receives replies to queries (DA, DSR, DECRQM, ...). Writes
	// happen after the terminal lock is released, from the goroutine that
	// called Write. May be nil.
	Responder io.Writer
	// Theme is the initial color scheme (default: theme.Default()).
	Theme *theme.Theme
	// OnBell and OnTitle are called with the terminal lock held; they must
	// not call back into the Terminal.
	OnBell  func()
	OnTitle func(string)
	// OnCommandDone is called when the shell reports a command finished
	// (OSC 133;D), with the lock held.
	OnCommandDone func(exit int, d time.Duration)
}

// MouseMode is the active mouse reporting mode.
type MouseMode uint8

const (
	MouseNone   MouseMode = iota
	MouseX10              // ?9: press only
	MouseNormal           // ?1000: press and release
	MouseButton           // ?1002: plus motion while a button is held
	MouseAny              // ?1003: all motion
)

// MouseEncoding is the mouse report format.
type MouseEncoding uint8

const (
	MouseEncDefault   MouseEncoding = iota
	MouseEncUTF8                    // ?1005
	MouseEncSGR                     // ?1006
	MouseEncURXVT                   // ?1015
	MouseEncSGRPixels               // ?1016
)

// CursorStyle is set by DECSCUSR.
type CursorStyle uint8

const (
	CursorDefault CursorStyle = iota
	CursorBlinkBlock
	CursorSteadyBlock
	CursorBlinkUnderline
	CursorSteadyUnderline
	CursorBlinkBar
	CursorSteadyBar
)

// Modes are terminal modes set by SM/RM and DECSET/DECRST.
type Modes struct {
	Insert          bool // IRM (4)
	LineFeedNewLine bool // LNM (20)
	AppCursor       bool // DECCKM (?1)
	ReverseVideo    bool // DECSCNM (?5)
	Origin          bool // DECOM (?6)
	Autowrap        bool // DECAWM (?7)
	CursorBlink     bool // ?12
	CursorVisible   bool // DECTCEM (?25)
	AppKeypad       bool // DECKPAM / DECNKM (?66)
	FocusEvents     bool // ?1004
	BracketedPaste  bool // ?2004
	SyncOutput      bool // ?2026
	AltScroll       bool // ?1007: wheel sends arrow keys on the alt screen
	Mouse           MouseMode
	MouseEncoding   MouseEncoding
}

type charset uint8

const (
	charsetASCII charset = iota
	charsetDECGraphics
)

// Cursor is the cursor position and the "pen" used for new cells.
type Cursor struct {
	X, Y int
	// Pen holds the attributes and colors applied to printed characters.
	Pen         Cell
	pendingWrap bool // DEC deferred wrap: at the right margin, wrap on next print
	charsets    [2]charset
	gl          int // active charset slot (SO/SI)
}

type savedCursor struct {
	cur    Cursor
	origin bool
	valid  bool
}

type screen struct {
	lines []Line
	saved savedCursor
	// kbd is the kitty keyboard protocol flag stack (each screen has its
	// own, per the spec). The top entry is in effect.
	kbd []uint8
}

// Terminal is the emulated terminal. All methods are safe for concurrent
// use; readers (e.g. the renderer) should hold Lock while inspecting state.
type Terminal struct {
	mu     sync.Mutex
	parser *vt.Parser
	opts   Options

	cols, rows  int
	primary     screen
	alt         screen
	active      *screen
	scrollback  *Scrollback
	cur         Cursor
	top, bottom int // scroll region, inclusive, 0-based
	tabs        []bool
	modes       Modes
	cursorStyle CursorStyle
	title       string
	lastRune    rune // for REP
	dirty       []bool
	allDirty    bool

	resp []byte // pending responses, flushed after unlocking

	// pushed counts lines ever pushed into scrollback. Together with the
	// scrollback length it gives every line a stable absolute row number,
	// so selections survive new output.
	pushed int64

	colors colorState

	// Shell integration state (OSC 7 and OSC 133).
	cwd   string
	shell shellMarks
}

// New creates a terminal of the given size.
func New(cols, rows int, opts Options) *Terminal {
	cols, rows = max(cols, 1), max(rows, 1)
	t := &Terminal{opts: opts, cols: cols, rows: rows}
	t.parser = vt.New((*handler)(t))
	t.scrollback = newScrollback(max(opts.Scrollback, 0))
	t.primary.lines = makeLines(cols, rows)
	t.alt.lines = makeLines(cols, rows)
	t.active = &t.primary
	t.dirty = make([]bool, rows)
	t.colors.base = opts.Theme
	if t.colors.base == nil {
		t.colors.base = theme.Default()
	}
	t.colors.recompute()
	t.resetState()
	return t
}

func makeLines(cols, rows int) []Line {
	cells := make([]Cell, cols*rows) // one allocation for the whole screen
	lines := make([]Line, rows)
	for i := range lines {
		lines[i].Cells = cells[i*cols : (i+1)*cols : (i+1)*cols]
	}
	return lines
}

// resetState puts modes, cursor, margins, and tabs in their power-on state
// without touching screen contents.
func (t *Terminal) resetState() {
	t.modes = Modes{Autowrap: true, CursorVisible: true, AltScroll: true}
	t.primary.kbd, t.alt.kbd = nil, nil
	t.cur = Cursor{}
	t.top, t.bottom = 0, t.rows-1
	t.resetTabs()
	t.cursorStyle = CursorDefault
	t.primary.saved, t.alt.saved = savedCursor{}, savedCursor{}
}

func (t *Terminal) resetTabs() {
	t.tabs = make([]bool, t.cols)
	for i := 8; i < t.cols; i += 8 {
		t.tabs[i] = true
	}
}

// Write feeds output from the child process into the terminal.
func (t *Terminal) Write(p []byte) (int, error) {
	t.mu.Lock()
	t.parser.Advance(p)
	var resp []byte
	if len(t.resp) > 0 {
		resp = append([]byte(nil), t.resp...)
		t.resp = t.resp[:0]
	}
	t.mu.Unlock()
	if resp != nil && t.opts.Responder != nil {
		if _, err := t.opts.Responder.Write(resp); err != nil {
			return len(p), err
		}
	}
	return len(p), nil
}

// Lock and Unlock guard the accessors below for consistent snapshots.
func (t *Terminal) Lock()   { t.mu.Lock() }
func (t *Terminal) Unlock() { t.mu.Unlock() }

// Size returns the grid size. Caller must hold the lock.
func (t *Terminal) Size() (cols, rows int) { return t.cols, t.rows }

// Cursor returns the cursor. Caller must hold the lock.
func (t *Terminal) Cursor() Cursor { return t.cur }

// Modes returns the current modes. Caller must hold the lock.
func (t *Terminal) Modes() Modes { return t.modes }

// CursorStyle returns the DECSCUSR style. Caller must hold the lock.
func (t *Terminal) CursorStyle() CursorStyle { return t.cursorStyle }

// Title returns the window title. Caller must hold the lock.
func (t *Terminal) Title() string { return t.title }

// AltScreen reports whether the alternate screen is active. Caller must
// hold the lock.
func (t *Terminal) AltScreen() bool { return t.active == &t.alt }

// Line returns row y of the active screen. Caller must hold the lock.
func (t *Terminal) Line(y int) *Line { return &t.active.lines[y] }

// Scrollback returns the scrollback buffer. Caller must hold the lock.
func (t *Terminal) Scrollback() *Scrollback { return t.scrollback }

// TakeDirty reports which rows changed since the last call and resets the
// tracking. If all is true every row should be redrawn and rows is not
// meaningful. Caller must hold the lock; rows is only valid until Unlock.
func (t *Terminal) TakeDirty() (rows []bool, all bool) {
	all = t.allDirty
	if all {
		clear(t.dirty)
	}
	t.allDirty = false
	return t.dirty, all
}

func (t *Terminal) markDirty(y int) { t.dirty[y] = true }

func (t *Terminal) markAllDirty() { t.allDirty = true }

func (t *Terminal) markRangeDirty(from, to int) {
	if t.allDirty {
		return
	}
	if from == 0 && to == t.rows-1 {
		t.allDirty = true
		return
	}
	for y := from; y <= to; y++ {
		t.dirty[y] = true
	}
}

// KeyboardFlags returns the kitty keyboard protocol flags in effect.
// Caller must hold the lock.
func (t *Terminal) KeyboardFlags() uint8 {
	if k := t.active.kbd; len(k) > 0 {
		return k[len(k)-1]
	}
	return 0
}

// ---- scrollback viewing and absolute rows ----------------------------
//
// An absolute row numbers every line ever shown: scrollback line i (0 =
// oldest retained) is row pushed-len+i, and screen row y is row pushed+y.
// A viewport scrolled back by offset lines shows rows starting at
// pushed-offset. All of these require the lock.

// ScrollbackLen returns how many lines can be scrolled back (0 on the
// alternate screen).
func (t *Terminal) ScrollbackLen() int {
	if t.active == &t.alt {
		return 0
	}
	return t.scrollback.Len()
}

// Pushed returns the number of lines ever pushed to scrollback.
func (t *Terminal) Pushed() int64 { return t.pushed }

// AbsRow returns the absolute row shown at viewport row y.
func (t *Terminal) AbsRow(y, offset int) int64 {
	return t.pushed - int64(offset) + int64(y)
}

// LineAt returns the line at an absolute row, or nil if it is no longer
// retained (or not yet written).
func (t *Terminal) LineAt(abs int64) *Line {
	rel := abs - t.pushed
	switch {
	case rel >= 0 && rel < int64(t.rows):
		return &t.active.lines[rel]
	case rel < 0 && t.active == &t.primary:
		i := int64(t.scrollback.Len()) + rel
		if i >= 0 {
			return t.scrollback.At(int(i))
		}
	}
	return nil
}

// TextRange extracts text between two absolute positions (inclusive, in
// reading order). Soft-wrapped lines are joined; hard line ends become
// newlines with trailing blanks trimmed.
func (t *Terminal) TextRange(startRow int64, startCol int, endRow int64, endCol int) string {
	var b strings.Builder
	for row := startRow; row <= endRow; row++ {
		l := t.LineAt(row)
		if l == nil {
			continue
		}
		from, to := 0, len(l.Cells)-1
		if row == startRow {
			from = startCol
		}
		if row == endRow {
			to = min(endCol, to)
		}
		var seg strings.Builder
		for x := max(from, 0); x <= to; x++ {
			c := l.Cells[x]
			switch {
			case c.Attrs&(AttrWideSpacer|attrWrapPad) != 0:
			case c.Rune == 0:
				seg.WriteByte(' ')
			default:
				seg.WriteRune(c.Rune)
			}
		}
		if l.Wrapped && row != endRow {
			b.WriteString(seg.String())
			continue
		}
		b.WriteString(strings.TrimRight(seg.String(), " "))
		if row != endRow {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// Text returns the visible screen as plain text, one line per row with
// trailing spaces trimmed. Intended for tests and debugging.
func (t *Terminal) Text() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	var b strings.Builder
	for y := range t.active.lines {
		if y > 0 {
			b.WriteByte('\n')
		}
		writeLineText(&b, &t.active.lines[y])
	}
	return b.String()
}

// ScrollbackText returns the scrollback as plain text, oldest first.
func (t *Terminal) ScrollbackText() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	var b strings.Builder
	for i := 0; i < t.scrollback.Len(); i++ {
		if i > 0 {
			b.WriteByte('\n')
		}
		writeLineText(&b, t.scrollback.At(i))
	}
	return b.String()
}

func writeLineText(b *strings.Builder, l *Line) {
	var s strings.Builder
	for _, c := range l.Cells {
		switch {
		case c.Attrs&AttrWideSpacer != 0:
		case c.Rune == 0:
			s.WriteByte(' ')
		default:
			s.WriteRune(c.Rune)
		}
	}
	b.WriteString(strings.TrimRight(s.String(), " "))
}

func (t *Terminal) respond(s string) { t.resp = append(t.resp, s...) }

// ---- primitive operations ----------------------------------------------

// blankCell is an erased cell: empty, with the current background color
// (xterm "back color erase").
func (t *Terminal) blankCell() Cell { return Cell{BG: t.cur.Pen.BG} }

// clearIn blanks l.Cells[from:to].
func (t *Terminal) clearIn(l *Line, from, to int) {
	b := t.blankCell()
	fillCells(l.Cells[from:to], b)
	if b != (Cell{}) {
		l.touch(to)
	}
}

// fillCells sets every cell to c. Zero cells use memclr; others use
// doubling copies, both far faster than a per-element loop.
func fillCells(cells []Cell, c Cell) {
	if len(cells) == 0 {
		return
	}
	if c == (Cell{}) {
		clear(cells)
		return
	}
	cells[0] = c
	for n := 1; n < len(cells); n *= 2 {
		copy(cells[n:], cells[:n])
	}
}

// resetLine prepares a recycled line as a blank row of the current width.
func (t *Terminal) resetLine(l Line) Line {
	if cap(l.Cells) < t.cols {
		l.Cells = make([]Cell, t.cols)
	}
	full := l.Cells[:cap(l.Cells)]
	if b := t.blankCell(); b == (Cell{}) {
		clear(full[:min(l.n, len(full))]) // only the written part is dirty
		l.n = 0
	} else {
		fillCells(full, b)
		l.n = len(full)
	}
	l.Cells = full[:t.cols]
	l.Wrapped, l.Prompt, l.HasExit, l.Exit = false, false, false, 0
	return l
}

// fixWide erases a double-width character that is about to be partially
// overwritten at column x.
func (t *Terminal) fixWide(l *Line, x int) {
	if x < 0 || x >= len(l.Cells) {
		return
	}
	c := &l.Cells[x]
	switch {
	case c.Attrs&AttrWideSpacer != 0 && x > 0:
		t.clearIn(l, x-1, x+1)
	case c.Attrs&AttrWide != 0 && x+1 < len(l.Cells):
		t.clearIn(l, x, x+2)
	}
}

// scrollUp scrolls the scroll region up by n lines. On the primary screen,
// lines leaving a region that starts at the top go to scrollback.
func (t *Terminal) scrollUp(n int) {
	top, bot := t.top, t.bottom
	n = min(n, bot-top+1)
	lines := t.active.lines
	toScrollback := t.active == &t.primary && top == 0
	for range n {
		old := lines[top]
		if toScrollback {
			old = t.scrollback.push(old)
			t.pushed++
		}
		copy(lines[top:bot], lines[top+1:bot+1])
		lines[bot] = t.resetLine(old)
	}
	t.markRangeDirty(top, bot)
}

// scrollDown scrolls the scroll region down by n lines.
func (t *Terminal) scrollDown(n int) {
	t.scrollDownFrom(t.top, n)
}

// scrollDownFrom inserts n blank lines at row y, pushing lines below it
// down to the bottom margin.
func (t *Terminal) scrollDownFrom(y, n int) {
	bot := t.bottom
	n = min(n, bot-y+1)
	lines := t.active.lines
	for range n {
		old := lines[bot]
		copy(lines[y+1:bot+1], lines[y:bot])
		lines[y] = t.resetLine(old)
	}
	t.markRangeDirty(y, bot)
}

// scrollUpFrom deletes n lines at row y, pulling lines up from the bottom
// margin. Lines never go to scrollback.
func (t *Terminal) scrollUpFrom(y, n int) {
	bot := t.bottom
	n = min(n, bot-y+1)
	lines := t.active.lines
	for range n {
		old := lines[y]
		copy(lines[y:bot], lines[y+1:bot+1])
		lines[bot] = t.resetLine(old)
	}
	t.markRangeDirty(y, bot)
}

// index moves the cursor down one line, scrolling at the bottom margin (IND/LF).
func (t *Terminal) index() {
	t.cur.pendingWrap = false
	switch {
	case t.cur.Y == t.bottom:
		t.scrollUp(1)
	case t.cur.Y < t.rows-1:
		t.cur.Y++
	}
}

// reverseIndex moves the cursor up one line, scrolling at the top margin (RI).
func (t *Terminal) reverseIndex() {
	t.cur.pendingWrap = false
	switch {
	case t.cur.Y == t.top:
		t.scrollDown(1)
	case t.cur.Y > 0:
		t.cur.Y--
	}
}

func (t *Terminal) carriageReturn() {
	t.cur.X = 0
	t.cur.pendingWrap = false
}

// wrap performs an autowrap: marks the line soft-wrapped and moves to the
// start of the next line.
func (t *Terminal) wrap() {
	t.active.lines[t.cur.Y].Wrapped = true
	t.carriageReturn()
	t.index()
}

// moveTo sets the cursor position. In origin mode y is relative to the top
// margin and confined to the scroll region.
func (t *Terminal) moveTo(x, y int) {
	if t.modes.Origin {
		y = clamp(y+t.top, t.top, t.bottom)
	} else {
		y = clamp(y, 0, t.rows-1)
	}
	t.cur.X = clamp(x, 0, t.cols-1)
	t.cur.Y = y
	t.cur.pendingWrap = false
}

func clamp(v, lo, hi int) int { return max(lo, min(v, hi)) }

// ---- printing ----------------------------------------------------------

var decGraphics = [...]rune{
	'_': ' ', '`': '◆', 'a': '▒', 'b': '␉', 'c': '␌', 'd': '␍', 'e': '␊',
	'f': '°', 'g': '±', 'h': '␤', 'i': '␋', 'j': '┘', 'k': '┐', 'l': '┌',
	'm': '└', 'n': '┼', 'o': '⎺', 'p': '⎻', 'q': '─', 'r': '⎼', 's': '⎽',
	't': '├', 'u': '┤', 'v': '┴', 'w': '┬', 'x': '│', 'y': '≤', 'z': '≥',
	'{': 'π', '|': '≠', '}': '£', '~': '·',
}

func (t *Terminal) print(r rune) {
	if t.cur.charsets[t.cur.gl] == charsetDECGraphics && r < rune(len(decGraphics)) && decGraphics[r] != 0 {
		r = decGraphics[r]
	}
	w := runeWidth(r)
	if w == 0 {
		// TODO: attach combining characters to the previous cell.
		return
	}
	t.lastRune = r

	c := &t.cur
	if c.pendingWrap && t.modes.Autowrap {
		t.wrap()
	}
	if w == 2 && c.X == t.cols-1 {
		if !t.modes.Autowrap || t.cols < 2 {
			return
		}
		l := &t.active.lines[c.Y]
		t.fixWide(l, c.X)
		pad := t.blankCell()
		pad.Attrs = attrWrapPad
		l.Cells[c.X] = pad
		l.touch(c.X + 1)
		t.wrap()
	}

	l := &t.active.lines[c.Y]
	if t.modes.Insert {
		t.insertCells(w)
	}
	t.fixWide(l, c.X)
	if w == 2 {
		t.fixWide(l, c.X+1)
	}
	cell := c.Pen
	cell.Rune = r
	if w == 2 {
		cell.Attrs |= AttrWide
		l.Cells[c.X] = cell
		spacer := c.Pen
		spacer.Attrs |= AttrWideSpacer
		l.Cells[c.X+1] = spacer
	} else {
		l.Cells[c.X] = cell
	}
	l.touch(c.X + w)
	t.markDirty(c.Y)

	if c.X+w >= t.cols {
		c.X = t.cols - 1
		c.pendingWrap = t.modes.Autowrap
	} else {
		c.X += w
	}
}

// printASCII is the fast path for runs of printable ASCII.
func (t *Terminal) printASCII(b []byte) {
	c := &t.cur
	if c.charsets[c.gl] != charsetASCII || t.modes.Insert {
		for _, ch := range b {
			t.print(rune(ch))
		}
		return
	}
	t.lastRune = rune(b[len(b)-1])
	for len(b) > 0 {
		if c.pendingWrap {
			t.wrap()
		}
		l := &t.active.lines[c.Y]
		n := min(len(b), t.cols-c.X)
		t.fixWide(l, c.X)
		t.fixWide(l, c.X+n-1)
		cells := l.Cells[c.X : c.X+n]
		fillCells(cells, c.Pen)
		for i, ch := range b[:n] {
			cells[i].Rune = rune(ch)
		}
		l.touch(c.X + n)
		t.markDirty(c.Y)
		b = b[n:]
		if c.X+n < t.cols {
			c.X += n
			continue
		}
		c.X = t.cols - 1
		if !t.modes.Autowrap {
			// Without autowrap, remaining characters overwrite the last
			// column; only the final one survives.
			if len(b) > 0 {
				l.Cells[c.X].Rune = rune(b[len(b)-1])
			}
			return
		}
		c.pendingWrap = true
	}
}

// ---- tabs ---------------------------------------------------------------

func (t *Terminal) tabForward(n int) {
	x := t.cur.X
	for ; n > 0 && x < t.cols-1; n-- {
		x++
		for x < t.cols-1 && !t.tabs[x] {
			x++
		}
	}
	t.cur.X = x
	t.cur.pendingWrap = false
}

func (t *Terminal) tabBackward(n int) {
	x := t.cur.X
	for ; n > 0 && x > 0; n-- {
		x--
		for x > 0 && !t.tabs[x] {
			x--
		}
	}
	t.cur.X = x
	t.cur.pendingWrap = false
}

// ---- editing ------------------------------------------------------------

// insertCells inserts n blank cells at the cursor, shifting the rest of
// the line right (ICH and insert mode).
func (t *Terminal) insertCells(n int) {
	l := &t.active.lines[t.cur.Y]
	x := t.cur.X
	n = min(n, t.cols-x)
	t.fixWide(l, x)
	copy(l.Cells[x+n:], l.Cells[x:])
	l.touch(t.cols)
	t.clearIn(l, x, x+n)
	// A wide character may have been split at the right edge.
	if last := &l.Cells[t.cols-1]; last.Attrs&AttrWide != 0 {
		*last = t.blankCell()
	}
	l.Wrapped = false
	t.cur.pendingWrap = false
	t.markDirty(t.cur.Y)
}

// deleteCells deletes n cells at the cursor, shifting the rest left (DCH).
func (t *Terminal) deleteCells(n int) {
	l := &t.active.lines[t.cur.Y]
	x := t.cur.X
	n = min(n, t.cols-x)
	t.fixWide(l, x)
	t.fixWide(l, x+n)
	copy(l.Cells[x:], l.Cells[x+n:])
	t.clearIn(l, t.cols-n, t.cols)
	l.Wrapped = false
	t.cur.pendingWrap = false
	t.markDirty(t.cur.Y)
}

// eraseCells blanks n cells starting at the cursor without shifting (ECH).
func (t *Terminal) eraseCells(n int) {
	l := &t.active.lines[t.cur.Y]
	x := t.cur.X
	end := min(x+n, t.cols)
	t.fixWide(l, x)
	t.fixWide(l, end-1)
	t.clearIn(l, x, end)
	t.cur.pendingWrap = false
	t.markDirty(t.cur.Y)
}

// eraseLine implements EL: 0 = cursor to end, 1 = start to cursor, 2 = all.
func (t *Terminal) eraseLine(mode int) {
	l := &t.active.lines[t.cur.Y]
	x := t.cur.X
	switch mode {
	case 0:
		t.fixWide(l, x)
		t.clearIn(l, x, t.cols)
		l.Wrapped = false
	case 1:
		t.fixWide(l, x)
		t.clearIn(l, 0, x+1)
	case 2:
		t.clearIn(l, 0, t.cols)
		l.Wrapped = false
	}
	t.cur.pendingWrap = false
	t.markDirty(t.cur.Y)
}

// eraseDisplay implements ED: 0 = cursor to end, 1 = start to cursor,
// 2 = whole screen, 3 = scrollback.
func (t *Terminal) eraseDisplay(mode int) {
	switch mode {
	case 0:
		t.eraseLine(0)
		for y := t.cur.Y + 1; y < t.rows; y++ {
			t.active.lines[y] = t.resetLine(t.active.lines[y])
		}
		t.markRangeDirty(t.cur.Y, t.rows-1)
	case 1:
		t.eraseLine(1)
		for y := 0; y < t.cur.Y; y++ {
			t.active.lines[y] = t.resetLine(t.active.lines[y])
		}
		t.markRangeDirty(0, t.cur.Y)
	case 2:
		for y := range t.active.lines {
			t.active.lines[y] = t.resetLine(t.active.lines[y])
		}
		t.markAllDirty()
	case 3:
		t.scrollback.clear()
	}
	t.cur.pendingWrap = false
}

// ---- cursor save/restore and screens ------------------------------------

func (t *Terminal) saveCursor() {
	t.active.saved = savedCursor{cur: t.cur, origin: t.modes.Origin, valid: true}
}

func (t *Terminal) restoreCursor() {
	s := t.active.saved
	if !s.valid {
		// Restoring without a save resets to home with default attributes.
		t.cur = Cursor{}
		t.modes.Origin = false
		return
	}
	t.cur = s.cur
	t.modes.Origin = s.origin
	t.cur.X = clamp(t.cur.X, 0, t.cols-1)
	t.cur.Y = clamp(t.cur.Y, 0, t.rows-1)
}

func (t *Terminal) enterAltScreen(clearIt bool) {
	if t.active == &t.alt {
		return
	}
	t.active = &t.alt
	if clearIt {
		for y := range t.alt.lines {
			t.alt.lines[y] = t.resetLine(t.alt.lines[y])
		}
	}
	t.markAllDirty()
}

func (t *Terminal) exitAltScreen() {
	if t.active == &t.primary {
		return
	}
	t.active = &t.primary
	t.markAllDirty()
}

// fullReset implements RIS.
func (t *Terminal) fullReset() {
	t.resetState()
	t.active = &t.primary
	for y := range t.primary.lines {
		t.primary.lines[y] = t.resetLine(t.primary.lines[y])
		t.alt.lines[y] = t.resetLine(t.alt.lines[y])
	}
	t.scrollback.clear()
	t.title = ""
	t.colors.reset()
	t.markAllDirty()
}

// softReset implements DECSTR.
func (t *Terminal) softReset() {
	t.modes.Insert = false
	t.modes.Origin = false
	t.modes.Autowrap = true
	t.modes.CursorVisible = true
	t.modes.AppCursor = false
	t.modes.AppKeypad = false
	t.top, t.bottom = 0, t.rows-1
	t.cur.Pen = Cell{}
	t.cur.charsets = [2]charset{}
	t.cur.gl = 0
	t.cur.pendingWrap = false
	t.active.saved = savedCursor{}
}

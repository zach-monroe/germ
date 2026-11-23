package term

// Resize changes the grid size. The primary screen and its scrollback are
// reflowed: soft-wrapped lines are re-joined and wrapped at the new width.
// The alternate screen is truncated or padded (full-screen apps redraw on
// SIGWINCH anyway).
func (t *Terminal) Resize(cols, rows int) {
	cols, rows = max(cols, 1), max(rows, 1)
	t.mu.Lock()
	defer t.mu.Unlock()
	if cols == t.cols && rows == t.rows {
		return
	}

	// The shared cursor belongs to whichever screen is active; the primary
	// screen's cursor lives in its saved slot while the alt screen is up.
	primaryCur := &t.cur
	if t.active == &t.alt {
		primaryCur = &t.primary.saved.cur
	}
	t.reflowPrimary(cols, rows, primaryCur)
	t.resizeAlt(cols, rows)

	t.cols, t.rows = cols, rows
	t.top, t.bottom = 0, rows-1
	t.resetTabs()
	t.dirty = make([]bool, rows)
	t.markAllDirty()

	clampCursor := func(c *Cursor) {
		c.X = clamp(c.X, 0, cols-1)
		c.Y = clamp(c.Y, 0, rows-1)
	}
	clampCursor(&t.cur)
	clampCursor(&t.primary.saved.cur)
	clampCursor(&t.alt.saved.cur)
}

func (t *Terminal) resizeAlt(cols, rows int) {
	lines := makeLines(cols, rows)
	for y := 0; y < min(rows, len(t.alt.lines)); y++ {
		copy(lines[y].Cells, t.alt.lines[y].Cells)
		fixClippedWide(lines[y].Cells)
		lines[y].n = cols
	}
	t.alt.lines = lines
	if t.active == &t.alt {
		t.cur.pendingWrap = false
	}
}

// fixClippedWide blanks a wide character whose spacer was cut off.
func fixClippedWide(cells []Cell) {
	if n := len(cells); n > 0 && cells[n-1].Attrs&AttrWide != 0 {
		cells[n-1] = Cell{BG: cells[n-1].BG}
	}
}

// reflowPrimary rewraps the primary screen plus scrollback to cols x rows
// and updates cur (the primary screen's cursor) to follow its text.
func (t *Terminal) reflowPrimary(cols, rows int, cur *Cursor) {
	sb := t.scrollback
	screen := t.primary.lines

	// Keep screen rows up to the cursor or the last non-blank row,
	// whichever is lower; blank rows below that are dropped.
	last := cur.Y
	for y := len(screen) - 1; y > last; y-- {
		if !screen[y].blank() {
			last = y
			break
		}
	}

	src := make([]*Line, 0, sb.Len()+last+1)
	for i := 0; i < sb.Len(); i++ {
		src = append(src, sb.At(i))
	}
	for y := 0; y <= last; y++ {
		src = append(src, &screen[y])
	}
	curLine := sb.Len() + cur.Y
	curOff := cur.X
	if cur.pendingWrap {
		curOff++
	}

	t.shell.invalidate() // absolute rows are renumbered below
	var (
		out     []Line
		logical []Cell
		lmeta   Line // marks of the logical line's first row
		curRow  = -1
		curCol  = 0
	)
	for i, l := range src {
		if len(logical) == 0 {
			lmeta = Line{Prompt: l.Prompt, HasExit: l.HasExit, Exit: l.Exit}
		}
		if i == curLine {
			// Cursor offset within the logical line being assembled.
			curOff += len(logical)
		}
		if l.Wrapped && i != len(src)-1 {
			for _, c := range l.Cells {
				if c.Attrs&attrWrapPad == 0 {
					logical = append(logical, c)
				}
			}
			continue
		}
		logical = append(logical, l.Cells[:l.textLen()]...)
		hasCursor := i >= curLine && curRow < 0
		first := len(out)
		out, curRow, curCol = rewrap(out, logical, cols, hasCursor, curOff, curRow, curCol)
		out[first].Prompt, out[first].HasExit, out[first].Exit = lmeta.Prompt, lmeta.HasExit, lmeta.Exit
		logical = logical[:0]
	}
	if curRow < 0 { // cursor below all content (shouldn't happen)
		curRow, curCol = len(out)-1, 0
	}

	// Choose the window of rows that becomes the screen: the bottom-most
	// rows, but always including the cursor.
	start := max(0, len(out)-rows)
	if curRow < start {
		start = curRow
	}
	end := min(len(out), start+rows)

	newSB := newScrollback(sb.Max())
	for i := max(0, start-sb.Max()); i < start; i++ {
		newSB.push(out[i])
	}
	lines := makeLines(cols, rows)
	for y := start; y < end; y++ {
		copy(lines[y-start].Cells, out[y].Cells)
		lines[y-start].Wrapped = out[y].Wrapped
		lines[y-start].Prompt, lines[y-start].HasExit, lines[y-start].Exit = out[y].Prompt, out[y].HasExit, out[y].Exit
		lines[y-start].n = cols
	}

	t.scrollback = newSB
	t.primary.lines = lines
	cur.Y = curRow - start
	cur.X = min(curCol, cols-1)
	cur.pendingWrap = false
}

// rewrap splits one logical line into rows of width cols and appends them
// to out. If hasCursor, it also maps cursor offset off to a (row, col).
func rewrap(out []Line, cells []Cell, cols int, hasCursor bool, off, curRow, curCol int) ([]Line, int, int) {
	row := make([]Cell, 0, cols)
	flush := func(wrapped bool) {
		full := make([]Cell, cols)
		copy(full, row)
		out = append(out, Line{Cells: full, Wrapped: wrapped, n: cols})
		row = row[:0]
	}
	for i := 0; i < len(cells); i++ {
		c := cells[i]
		wide := c.Attrs&AttrWide != 0 && i+1 < len(cells)
		if wide && len(row) == cols-1 && cols > 1 {
			row = append(row, Cell{Attrs: attrWrapPad})
			flush(true)
		} else if len(row) == cols {
			flush(true)
		}
		if hasCursor && i == off {
			curRow, curCol = len(out), len(row)
		}
		row = append(row, c)
		if wide && cols > 1 {
			i++
			row = append(row, cells[i])
		}
	}
	if hasCursor && off >= len(cells) {
		// Cursor sits past the end of the text (e.g. after a prompt's
		// trailing space).
		col := len(row) + off - len(cells)
		if col >= cols && len(row) == cols {
			flush(true)
			col -= cols
		}
		curRow, curCol = len(out), min(col, cols-1)
	}
	flush(false)
	return out, curRow, curCol
}

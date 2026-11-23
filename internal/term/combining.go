package term

import (
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// combine attaches a zero-width character to the character before the
// cursor: precomposed when Unicode has a form for the pair (e + ◌́ = é),
// otherwise kept as the cell's Mark. Joiners and variation selectors only
// matter for emoji sequences, which germ draws one cell at a time, so
// they're dropped.
func (t *Terminal) combine(r rune) {
	if !unicode.In(r, unicode.Mn, unicode.Me, unicode.Mc) {
		return
	}
	c := &t.cur
	x := c.X - 1
	if c.pendingWrap {
		x = c.X // the cursor still sits on the last printed cell
	}
	l := &t.active.lines[c.Y]
	if x < 0 || x >= len(l.Cells) {
		return
	}
	cell := &l.Cells[x]
	if cell.Attrs&AttrWideSpacer != 0 && x > 0 {
		cell = &l.Cells[x-1]
	}
	if cell.Rune == 0 {
		return
	}
	if cell.Mark == 0 {
		if s := norm.NFC.String(string([]rune{cell.Rune, r})); len([]rune(s)) == 1 {
			cell.Rune = []rune(s)[0]
			t.markDirty(c.Y)
			return
		}
	} else if s := norm.NFC.String(string([]rune{cell.Rune, cell.Mark, r})); len([]rune(s)) == 2 {
		// e.g. a second mark that composes with the first
		rs := []rune(s)
		cell.Rune, cell.Mark = rs[0], rs[1]
		t.markDirty(c.Y)
		return
	}
	cell.Mark = r // one mark per cell; a later one replaces it
	t.markDirty(c.Y)
}

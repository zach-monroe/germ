package app

import (
	"fmt"

	"github.com/go-gl/glfw/v3.3/glfw"
)

// Dragging the separator between panes resizes them: native tabs change
// the split's ratio; tmux tabs ask tmux to move the border (resize-pane),
// and the new layout comes back as usual.

// splitAt finds the split whose separator covers content cell (x, y).
func splitAt(n *node, x, y int) *node {
	if n == nil || n.isLeaf() {
		return nil
	}
	r := n.rect
	switch n.dir {
	case splitH:
		if sx := n.a.rect.X + n.a.rect.W; x == sx && y >= r.Y && y < r.Y+r.H {
			return n
		}
	case splitV:
		if sy := n.a.rect.Y + n.a.rect.H; y == sy && x >= r.X && x < r.X+r.W {
			return n
		}
	}
	if s := splitAt(n.a, x, y); s != nil {
		return s
	}
	return splitAt(n.b, x, y)
}

type sepDrag struct {
	split *node
	tab   *tab
	pos   int // separator's current column/row
}

// startSepDrag begins a drag if (gx, gy) is on a separator. Grid
// coordinates include the tab bar.
func (a *App) startSepDrag(gx, gy int) bool {
	t := a.tab()
	if t == nil || t.zoomed != nil || t.root == nil {
		return false
	}
	y := gy - a.barRows()
	s := splitAt(t.root, gx, y)
	if s == nil {
		return false
	}
	pos := s.a.rect.X + s.a.rect.W
	if s.dir == splitV {
		pos = s.a.rect.Y + s.a.rect.H
	}
	a.mouse.sep = &sepDrag{split: s, tab: t, pos: pos}
	a.setResizeCursor(s.dir)
	return true
}

// dragSep moves the separator being dragged to (gx, gy).
func (a *App) dragSep(gx, gy int) {
	d := a.mouse.sep
	s, r := d.split, d.split.rect
	want := gx
	lo, hi := r.X+1, r.X+r.W-2 // keep a cell on each side
	if s.dir == splitV {
		want = gy - a.barRows()
		lo, hi = r.Y+1, r.Y+r.H-2
	}
	want = max(lo, min(want, hi))
	if want == d.pos {
		return
	}
	delta := want - d.pos
	d.pos = want
	if !d.tab.fixed {
		total := r.W - 1
		start := r.X
		if s.dir == splitV {
			total, start = r.H-1, r.Y
		}
		s.ratio = float64(want-start) / float64(total)
		a.relayout()
		return
	}
	// tmux: move the border of the pane just before the separator.
	if a.tmux == nil {
		return
	}
	p := a.paneBefore(d.tab, s, gx, gy-a.barRows())
	if p == nil {
		return
	}
	b, ok := p.be.(*tmuxBackend)
	if !ok {
		return
	}
	flag := "-R"
	if s.dir == splitV {
		flag = "-D"
	}
	if delta < 0 {
		flag, delta = map[string]string{"-R": "-L", "-D": "-U"}[flag], -delta
	}
	a.tmux.command(fmt.Sprintf("resize-pane -t %%%d %s %d", b.id, flag, delta), nil)
}

// paneBefore is the pane left of (or above) split s's separator, on the
// pointer's row (column).
func (a *App) paneBefore(t *tab, s *node, x, y int) *pane {
	var best *pane
	for _, n := range s.a.leaves(nil) {
		p := t.panes[n.id]
		if p == nil {
			continue
		}
		r := p.rect
		if s.dir == splitH && r.X+r.W == s.a.rect.X+s.a.rect.W && y >= r.Y && y < r.Y+r.H {
			return p
		}
		if s.dir == splitV && r.Y+r.H == s.a.rect.Y+s.a.rect.H && x >= r.X && x < r.X+r.W {
			return p
		}
		best = p
	}
	return best
}

func (a *App) endSepDrag() {
	a.mouse.sep = nil
	a.setResizeCursor(255)
}

// setResizeCursor shows a resize arrow while dragging (255: normal).
func (a *App) setResizeCursor(dir splitDir) {
	var shape glfw.StandardCursor
	switch dir {
	case splitH:
		shape = glfw.HResizeCursor
	case splitV:
		shape = glfw.VResizeCursor
	default:
		a.win.SetCursor(nil)
		a.handShown = false
		return
	}
	if a.resizeCursors == nil {
		a.resizeCursors = map[glfw.StandardCursor]*glfw.Cursor{}
	}
	c := a.resizeCursors[shape]
	if c == nil {
		c = glfw.CreateStandardCursor(shape)
		a.resizeCursors[shape] = c
	}
	a.win.SetCursor(c)
}

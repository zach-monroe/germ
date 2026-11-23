package app

// Pane layout: a binary split tree measured in cells, with a one-cell
// separator between siblings — the same model tmux uses, so control-mode
// layouts map onto it exactly.

// cellRect is a rectangle in grid cells.
type cellRect struct{ X, Y, W, H int }

func (r cellRect) contains(x, y int) bool {
	return x >= r.X && x < r.X+r.W && y >= r.Y && y < r.Y+r.H
}

// splitDir is the orientation of a split.
type splitDir uint8

const (
	splitH splitDir = iota // side by side (left | right)
	splitV                 // stacked (top / bottom)
)

// sepLine is a separator: a column (vertical) or row (horizontal) of cells.
type sepLine struct {
	Vertical bool
	X, Y     int // first cell
	Len      int
}

// node is a layout tree node: a leaf holding a pane ID, or a split.
type node struct {
	id     int // leaf pane ID (when a == nil)
	dir    splitDir
	a, b   *node
	ratio  float64 // share of space for a
	parent *node
	rect   cellRect
}

func leaf(id int) *node { return &node{id: id} }

func (n *node) isLeaf() bool { return n.a == nil }

// layout assigns rectangles to n and its descendants.
func (n *node) layout(r cellRect) {
	n.rect = r
	if n.isLeaf() {
		return
	}
	switch n.dir {
	case splitH:
		aw := sizeFor(r.W, n.ratio)
		n.a.layout(cellRect{r.X, r.Y, aw, r.H})
		n.b.layout(cellRect{r.X + aw + 1, r.Y, r.W - aw - 1, r.H})
	case splitV:
		ah := sizeFor(r.H, n.ratio)
		n.a.layout(cellRect{r.X, r.Y, r.W, ah})
		n.b.layout(cellRect{r.X, r.Y + ah + 1, r.W, r.H - ah - 1})
	}
}

// sizeFor splits total cells (minus a separator) by ratio, keeping at
// least one cell on each side when possible.
func sizeFor(total int, ratio float64) int {
	avail := total - 1
	if avail < 2 {
		return max(0, avail/2)
	}
	return max(1, min(int(float64(avail)*ratio+0.5), avail-1))
}

// leaves returns leaf nodes in order (left-to-right, top-to-bottom).
func (n *node) leaves(out []*node) []*node {
	if n.isLeaf() {
		return append(out, n)
	}
	return n.b.leaves(n.a.leaves(out))
}

// find returns the leaf with the given pane ID.
func (n *node) find(id int) *node {
	if n.isLeaf() {
		if n.id == id {
			return n
		}
		return nil
	}
	if f := n.a.find(id); f != nil {
		return f
	}
	return n.b.find(id)
}

// separators lists the separator lines of the laid-out tree.
func (n *node) separators(out []sepLine) []sepLine {
	if n.isLeaf() {
		return out
	}
	r := n.rect
	switch n.dir {
	case splitH:
		out = append(out, sepLine{Vertical: true, X: n.a.rect.X + n.a.rect.W, Y: r.Y, Len: r.H})
	case splitV:
		out = append(out, sepLine{Vertical: false, X: r.X, Y: n.a.rect.Y + n.a.rect.H, Len: r.W})
	}
	return n.b.separators(n.a.separators(out))
}

// split replaces leaf l with a split containing l and a new leaf newID
// (placed right of or below l). It returns the new root.
func split(root, l *node, dir splitDir, newID int) *node {
	nl := leaf(newID)
	s := &node{dir: dir, a: l, b: nl, ratio: 0.5, parent: l.parent}
	nl.parent = s
	if p := l.parent; p != nil {
		if p.a == l {
			p.a = s
		} else {
			p.b = s
		}
	} else {
		root = s
	}
	l.parent = s
	return root
}

// remove deletes leaf l, letting its sibling take the space. It returns
// the new root (nil if l was the only leaf).
func remove(root, l *node) *node {
	p := l.parent
	if p == nil {
		return nil
	}
	sib := p.a
	if sib == l {
		sib = p.b
	}
	sib.parent = p.parent
	if gp := p.parent; gp != nil {
		if gp.a == p {
			gp.a = sib
		} else {
			gp.b = sib
		}
		return root
	}
	return sib
}

// direction for focus movement.
type direction uint8

const (
	dirLeft direction = iota
	dirRight
	dirUp
	dirDown
)

// neighbor finds the leaf adjacent to from in direction d, preferring the
// one with the most overlap along the other axis.
func neighbor(root, from *node, d direction) *node {
	f := from.rect
	var best *node
	bestOverlap := 0
	for _, c := range root.leaves(nil) {
		if c == from {
			continue
		}
		r := c.rect
		var adjacent bool
		var overlap int
		switch d {
		case dirLeft:
			adjacent = r.X+r.W+1 == f.X
			overlap = spanOverlap(r.Y, r.H, f.Y, f.H)
		case dirRight:
			adjacent = f.X+f.W+1 == r.X
			overlap = spanOverlap(r.Y, r.H, f.Y, f.H)
		case dirUp:
			adjacent = r.Y+r.H+1 == f.Y
			overlap = spanOverlap(r.X, r.W, f.X, f.W)
		case dirDown:
			adjacent = f.Y+f.H+1 == r.Y
			overlap = spanOverlap(r.X, r.W, f.X, f.W)
		}
		if adjacent && overlap > bestOverlap {
			best, bestOverlap = c, overlap
		}
	}
	return best
}

func spanOverlap(a, alen, b, blen int) int {
	return max(0, min(a+alen, b+blen)-max(a, b))
}

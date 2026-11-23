package app

import "testing"

func TestLayoutSplits(t *testing.T) {
	root := leaf(1)
	root = split(root, root.find(1), splitH, 2)
	root = split(root, root.find(2), splitV, 3)
	root.layout(cellRect{0, 0, 81, 24})

	// 1 | 2
	//   | -
	//   | 3
	want := map[int]cellRect{
		1: {0, 0, 40, 24},
		2: {41, 0, 40, 12},
		3: {41, 13, 40, 11},
	}
	for id, r := range want {
		if got := root.find(id).rect; got != r {
			t.Errorf("pane %d: %+v, want %+v", id, got, r)
		}
	}
	seps := root.separators(nil)
	if len(seps) != 2 || !seps[0].Vertical || seps[0].X != 40 || seps[1].Vertical || seps[1].Y != 12 || seps[1].X != 41 {
		t.Errorf("separators: %+v", seps)
	}

	if n := neighbor(root, root.find(1), dirRight); n == nil || n.id != 2 {
		t.Errorf("right of 1 should be 2 (larger overlap), got %+v", n)
	}
	if n := neighbor(root, root.find(3), dirLeft); n == nil || n.id != 1 {
		t.Errorf("left of 3 should be 1")
	}
	if n := neighbor(root, root.find(2), dirDown); n == nil || n.id != 3 {
		t.Errorf("below 2 should be 3")
	}
	if n := neighbor(root, root.find(1), dirLeft); n != nil {
		t.Errorf("nothing left of 1, got %d", n.id)
	}
}

func TestLayoutRemove(t *testing.T) {
	root := leaf(1)
	root = split(root, root, splitH, 2)
	root = split(root, root.find(2), splitV, 3)
	root = remove(root, root.find(2))
	root.layout(cellRect{0, 0, 81, 24})
	if got := root.find(3).rect; got != (cellRect{41, 0, 40, 24}) {
		t.Errorf("3 should take 2's space: %+v", got)
	}
	root = remove(root, root.find(1))
	if !root.isLeaf() || root.id != 3 || root.parent != nil {
		t.Errorf("root should be leaf 3")
	}
	if remove(root, root) != nil {
		t.Error("removing the last pane should leave nil")
	}
}

func TestSizeForTiny(t *testing.T) {
	for _, total := range []int{1, 2, 3, 4, 100} {
		a := sizeFor(total, 0.5)
		if a < 0 || a+1 > total && total > 1 {
			t.Errorf("total %d -> %d", total, a)
		}
	}
}

func TestSplitAtAndDragRatio(t *testing.T) {
	root := split(leaf(1), leaf(1), splitH, 2)
	root.layout(cellRect{0, 0, 81, 10})
	sep := root.a.rect.X + root.a.rect.W
	if s := splitAt(root, sep, 3); s != root {
		t.Fatalf("splitAt(%d,3) = %v", sep, s)
	}
	if s := splitAt(root, sep-1, 3); s != nil {
		t.Error("found a split inside a pane")
	}
	// Dragging to column 20 gives the left pane 20 cells.
	root.ratio = float64(20-root.rect.X) / float64(root.rect.W-1)
	root.layout(root.rect)
	if root.a.rect.W != 20 {
		t.Errorf("left width %d", root.a.rect.W)
	}
	// Nested: a vertical split inside the right pane.
	right := root.b
	root2 := split(root, right, splitV, 3)
	root2.layout(cellRect{0, 0, 81, 10})
	r := root2.find(3).rect
	if s := splitAt(root2, r.X+1, r.Y-1); s == nil || s.dir != splitV {
		t.Errorf("nested split not found: %+v", s)
	}
}

package app

import (
	"reflect"
	"testing"
)

func TestUnescapeOutput(t *testing.T) {
	got := string(unescapeOutput(`\033[1mhi\033\134 \015\012é\134x`))
	want := "\x1b[1mhi\x1b\\ \r\né\\x"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestParseLayout(t *testing.T) {
	n, err := parseLayout("b25d,80x24,0,0{40x24,0,0,1,39x24,41,0[39x12,41,0,2,39x11,41,13,3]}")
	if err != nil {
		t.Fatal(err)
	}
	if got := n.panes(nil); !reflect.DeepEqual(got, []int{1, 2, 3}) {
		t.Errorf("panes = %v", got)
	}
	tree := n.toTree(func(p int) int { return p * 10 })
	if r := tree.find(30).rect; r != (cellRect{41, 13, 39, 11}) {
		t.Errorf("pane 3 rect %+v", r)
	}
	seps := tree.separators(nil)
	if len(seps) != 2 || !seps[0].Vertical || seps[0].X != 40 || seps[1].Vertical || seps[1].Y != 12 {
		t.Errorf("separators %+v", seps)
	}

	// Single pane, and a three-way horizontal split.
	if n, err := parseLayout("abcd,80x24,0,0,7"); err != nil || n.pane != 7 {
		t.Errorf("single: %v %+v", err, n)
	}
	n3, err := parseLayout("1234,80x24,0,0{26x24,0,0,1,26x24,27,0,2,26x24,54,0,3}")
	if err != nil {
		t.Fatal(err)
	}
	tr := n3.toTree(func(p int) int { return p })
	if s := tr.separators(nil); len(s) != 2 || s[0].X != 26 || s[1].X != 53 {
		t.Errorf("3-way separators %+v", s)
	}
	for _, bad := range []string{"", "x", "abcd,80x24,0,0", "abcd,80x24,0,0{1x1,0,0,1", "abcd,80x24,0,0,1junk"} {
		if _, err := parseLayout(bad); err == nil {
			t.Errorf("%q: expected error", bad)
		}
	}
}

func TestTmuxQuote(t *testing.T) {
	if got := tmuxQuote("it's /tmp"); got != `'it'\''s /tmp'` {
		t.Errorf("got %s", got)
	}
}

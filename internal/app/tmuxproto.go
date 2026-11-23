package app

import (
	"fmt"
	"strconv"
	"strings"
)

// tmux control-mode protocol helpers (pure; no I/O).

// unescapeOutput decodes a %output value: tmux escapes bytes below 0x20
// and backslash as \ooo octal; everything else is raw.
func unescapeOutput(s string) []byte {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\\' && isOctal(s, i+1) {
			v, _ := strconv.ParseUint(s[i+1:i+4], 8, 8)
			out = append(out, byte(v))
			i += 3
			continue
		}
		out = append(out, c)
	}
	return out
}

func isOctal(s string, i int) bool {
	if i+3 > len(s) {
		return false
	}
	for _, c := range s[i : i+3] {
		if c < '0' || c > '7' {
			return false
		}
	}
	return true
}

// layoutNode is a parsed tmux layout cell.
type layoutNode struct {
	rect     cellRect
	pane     int  // pane ID for leaves, -1 otherwise
	vertical bool // children stacked top-to-bottom ([...]); else left-to-right ({...})
	children []*layoutNode
}

// parseLayout parses a tmux layout string such as
// "b25d,80x24,0,0{40x24,0,0,1,39x24,41,0[39x12,41,0,2,39x11,41,13,3]}".
func parseLayout(s string) (*layoutNode, error) {
	comma := strings.IndexByte(s, ',')
	if comma < 0 {
		return nil, fmt.Errorf("tmux layout %q: no checksum", s)
	}
	p := &layoutParser{s: s, i: comma + 1}
	n, err := p.cell()
	if err != nil {
		return nil, fmt.Errorf("tmux layout %q: %w", s, err)
	}
	if p.i != len(s) {
		return nil, fmt.Errorf("tmux layout %q: trailing data", s)
	}
	return n, nil
}

type layoutParser struct {
	s string
	i int
}

func (p *layoutParser) num() (int, error) {
	start := p.i
	for p.i < len(p.s) && p.s[p.i] >= '0' && p.s[p.i] <= '9' {
		p.i++
	}
	if start == p.i {
		return 0, fmt.Errorf("expected number at %d", start)
	}
	return strconv.Atoi(p.s[start:p.i])
}

func (p *layoutParser) expect(c byte) error {
	if p.i >= len(p.s) || p.s[p.i] != c {
		return fmt.Errorf("expected %q at %d", c, p.i)
	}
	p.i++
	return nil
}

func (p *layoutParser) cell() (*layoutNode, error) {
	var n layoutNode
	var err error
	if n.rect.W, err = p.num(); err != nil {
		return nil, err
	}
	if err = p.expect('x'); err != nil {
		return nil, err
	}
	if n.rect.H, err = p.num(); err != nil {
		return nil, err
	}
	if err = p.expect(','); err != nil {
		return nil, err
	}
	if n.rect.X, err = p.num(); err != nil {
		return nil, err
	}
	if err = p.expect(','); err != nil {
		return nil, err
	}
	if n.rect.Y, err = p.num(); err != nil {
		return nil, err
	}
	n.pane = -1
	if p.i >= len(p.s) {
		return nil, fmt.Errorf("truncated")
	}
	switch p.s[p.i] {
	case ',':
		p.i++
		if n.pane, err = p.num(); err != nil {
			return nil, err
		}
	case '{', '[':
		n.vertical = p.s[p.i] == '['
		closer := byte('}')
		if n.vertical {
			closer = ']'
		}
		p.i++
		for {
			c, err := p.cell()
			if err != nil {
				return nil, err
			}
			n.children = append(n.children, c)
			if p.i < len(p.s) && p.s[p.i] == ',' {
				p.i++
				continue
			}
			if err := p.expect(closer); err != nil {
				return nil, err
			}
			break
		}
	default:
		return nil, fmt.Errorf("unexpected %q at %d", p.s[p.i], p.i)
	}
	return &n, nil
}

// panes lists the pane IDs in a layout.
func (n *layoutNode) panes(out []int) []int {
	if n.pane >= 0 {
		return append(out, n.pane)
	}
	for _, c := range n.children {
		out = c.panes(out)
	}
	return out
}

// toTree converts a tmux layout to a split tree with fixed rectangles.
// Leaf IDs are germ pane IDs, looked up with idFor. n-ary splits become
// right-leaning binary nodes whose rects cover their children.
func (n *layoutNode) toTree(idFor func(tmuxPane int) int) *node {
	if n.pane >= 0 {
		l := leaf(idFor(n.pane))
		l.rect = n.rect
		return l
	}
	return buildChain(n.children, n.vertical, idFor)
}

func buildChain(children []*layoutNode, vertical bool, idFor func(int) int) *node {
	if len(children) == 1 {
		return children[0].toTree(idFor)
	}
	a := children[0].toTree(idFor)
	b := buildChain(children[1:], vertical, idFor)
	dir := splitH
	if vertical {
		dir = splitV
	}
	s := &node{dir: dir, a: a, b: b}
	a.parent, b.parent = s, s
	s.rect = union(a.rect, b.rect)
	return s
}

func union(a, b cellRect) cellRect {
	x0, y0 := min(a.X, b.X), min(a.Y, b.Y)
	x1, y1 := max(a.X+a.W, b.X+b.W), max(a.Y+a.H, b.Y+b.H)
	return cellRect{x0, y0, x1 - x0, y1 - y0}
}

// tmuxQuote quotes an argument for a tmux command line.
func tmuxQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// parseTmuxID parses "%12", "@3", or "$1".
func parseTmuxID(s string) (int, bool) {
	if len(s) < 2 {
		return 0, false
	}
	v, err := strconv.Atoi(s[1:])
	return v, err == nil
}

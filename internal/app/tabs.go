package app

import (
	"fmt"
	"os"
	"strconv"

	"github.com/zach-monroe/germ/internal/render"
	"github.com/zach-monroe/germ/internal/term"
	"github.com/zach-monroe/germ/internal/theme"
)

// tab is a set of panes arranged by a layout tree.
type tab struct {
	root   *node
	panes  map[int]*pane
	active *pane
	zoomed *pane // shown alone when set

	// fixed layouts come from tmux (control mode): rects are set by the
	// tmux controller, not computed from ratios.
	fixed  bool
	tmuxID string
	name   string // tmux window name
}

func newTab(p *pane) *tab {
	return &tab{root: leaf(p.id), panes: map[int]*pane{p.id: p}, active: p}
}

func (t *tab) title() string {
	if t.name != "" {
		return t.name
	}
	if t.active != nil {
		return t.active.titleString()
	}
	return "germ"
}

// visible returns the panes to draw.
func (t *tab) visible() []*pane {
	if t.zoomed != nil {
		return []*pane{t.zoomed}
	}
	var out []*pane
	for _, n := range t.root.leaves(nil) {
		if p := t.panes[n.id]; p != nil {
			out = append(out, p)
		}
	}
	return out
}

func (a *App) tab() *tab {
	if len(a.tabs) == 0 {
		return nil
	}
	return a.tabs[a.cur]
}

// active returns the focused pane (nil only during shutdown).
func (a *App) active() *pane {
	if t := a.tab(); t != nil {
		return t.active
	}
	return nil
}

// barRows is the number of grid rows used by the tab bar.
func (a *App) barRows() int {
	if len(a.tabs) > 1 && a.gridRows > 2 {
		return 1
	}
	return 0
}

// content is the grid area for panes.
func (a *App) content() cellRect {
	return cellRect{0, 0, a.gridCols, a.gridRows - a.barRows()}
}

// relayout recomputes pane rectangles for every tab and resizes terminals
// whose size changed.
func (a *App) relayout() {
	area := a.content()
	for _, t := range a.tabs {
		if !t.fixed {
			t.root.layout(area)
			for _, n := range t.root.leaves(nil) {
				if p := t.panes[n.id]; p != nil {
					p.rect = n.rect
				}
			}
		}
		if t.zoomed != nil && !t.fixed {
			t.zoomed.rect = area
		}
		for _, p := range t.panes {
			a.resizePane(p, p.rect.W, p.rect.H)
		}
	}
	a.forceRedraw = true
}

func (a *App) resizePane(p *pane, cols, rows int) {
	cols, rows = max(cols, 1), max(rows, 1)
	p.term.Lock()
	c, r := p.term.Size()
	p.term.Unlock()
	if c == cols && r == rows {
		return
	}
	p.term.Resize(cols, rows)
	p.sel.clear() // reflow renumbers absolute rows
	p.offset = 0
	p.term.Lock()
	p.lastPushed = p.term.Pushed()
	p.term.Unlock()
	if !a.tmuxOwned(p) {
		p.be.Resize(cols, rows)
	}
}

// tmuxOwned reports whether the pane's size is dictated by tmux.
func (a *App) tmuxOwned(p *pane) bool {
	_, ok := p.be.(*tmuxBackend)
	return ok
}

// setActive focuses p within its tab.
func (a *App) setActive(t *tab, p *pane) {
	if t.active == p {
		return
	}
	old := t.active
	t.active = p
	if t.zoomed != nil && t.zoomed != p {
		t.zoomed = nil
		a.relayout()
	}
	a.focusChanged(old, false)
	a.focusChanged(p, true)
	a.forceRedraw = true
	if a.tmux != nil {
		a.tmux.selectPane(p)
	}
}

// focusChanged reports focus to a pane that asked for focus events.
func (a *App) focusChanged(p *pane, focused bool) {
	if p == nil {
		return
	}
	p.term.Lock()
	report := p.term.Modes().FocusEvents
	p.term.Unlock()
	if report {
		p.send(inputFocus(focused && a.focused))
	}
}

func (a *App) switchTab(i int) {
	if len(a.tabs) == 0 {
		return
	}
	i = (i%len(a.tabs) + len(a.tabs)) % len(a.tabs)
	if i == a.cur {
		return
	}
	old := a.active()
	a.cur = i
	a.focusChanged(old, false)
	a.focusChanged(a.active(), true)
	a.forceRedraw = true
	if a.tmux != nil {
		a.tmux.selectWindow(a.tab())
	}
}

// newPaneCommand is what new tabs and splits run.
func (a *App) newPaneCommand() []string {
	if len(a.opts.ShellCommand) > 0 {
		return a.opts.ShellCommand
	}
	return a.opts.Command
}

func (a *App) actionNewTab() {
	if a.tmux != nil {
		a.tmux.newWindow(a.activeCwd())
		return
	}
	area := a.content()
	if len(a.tabs) == 1 && a.gridRows > 2 {
		area.H-- // the tab bar appears
	}
	p, err := a.newLocalPane(a.newPaneCommand(), a.activeCwd(), area.W, area.H)
	if err != nil {
		fmt.Fprintln(os.Stderr, "germ:", err)
		return
	}
	old := a.active()
	a.tabs = append(a.tabs, newTab(p))
	a.cur = len(a.tabs) - 1
	a.focusChanged(old, false)
	a.relayout()
}

func (a *App) actionSplit(dir splitDir) {
	t := a.tab()
	if a.tmux != nil {
		a.tmux.splitPane(t.active, dir, a.activeCwd())
		return
	}
	cur := t.active
	n := t.root.find(cur.id)
	if n == nil {
		return
	}
	// Size the new pane from the split we're about to make.
	w, h := cur.rect.W, cur.rect.H
	if dir == splitH {
		w = cur.rect.W - sizeFor(cur.rect.W, 0.5) - 1
	} else {
		h = cur.rect.H - sizeFor(cur.rect.H, 0.5) - 1
	}
	p, err := a.newLocalPane(a.newPaneCommand(), a.activeCwd(), w, h)
	if err != nil {
		fmt.Fprintln(os.Stderr, "germ:", err)
		return
	}
	t.zoomed = nil
	t.panes[p.id] = p
	t.root = split(t.root, n, dir, p.id)
	a.relayout()
	a.setActive(t, p)
}

func (a *App) activeCwd() string {
	if p := a.active(); p != nil {
		return p.cwd()
	}
	return ""
}

func (a *App) actionFocus(d direction) bool {
	t := a.tab()
	if t == nil || t.zoomed != nil {
		return false
	}
	n := t.root.find(t.active.id)
	if n == nil {
		return false
	}
	if nb := neighbor(t.root, n, d); nb != nil {
		a.setActive(t, t.panes[nb.id])
	}
	return true
}

func (a *App) actionZoom() {
	t := a.tab()
	if a.tmux != nil {
		a.tmux.zoomPane(t.active)
		return
	}
	if len(t.panes) < 2 {
		return
	}
	if t.zoomed != nil {
		t.zoomed = nil
	} else {
		t.zoomed = t.active
	}
	a.relayout()
}

// closePaneProcess ends the active pane; it disappears once its process
// exits (handled by reapPanes).
func (a *App) actionClosePane() {
	if p := a.active(); p != nil {
		p.be.Close()
	}
}

func (a *App) actionCloseTab() {
	if t := a.tab(); t != nil {
		if a.tmux != nil && t.tmuxID != "" {
			a.tmux.killWindow(t)
			return
		}
		for _, p := range t.panes {
			p.be.Close()
		}
	}
}

// reapPanes removes panes whose process exited. It returns false when the
// last pane is gone and the app should quit.
func (a *App) reapPanes() bool {
	for _, p := range a.panes {
		if p.exited.Load() {
			a.removePane(p)
		}
	}
	return len(a.tabs) > 0
}

// removePane drops p from its tab (and the tab if it becomes empty).
func (a *App) removePane(p *pane) {
	a.lastExited = p
	delete(a.panes, p.id)
	for i, t := range a.tabs {
		if t.panes[p.id] == nil {
			continue
		}
		delete(t.panes, p.id)
		if t.zoomed == p {
			t.zoomed = nil
		}
		if n := t.root.find(p.id); n != nil && !t.fixed {
			t.root = remove(t.root, n)
		}
		if len(t.panes) == 0 {
			a.tabs = append(a.tabs[:i], a.tabs[i+1:]...)
			if a.cur >= len(a.tabs) {
				a.cur = max(0, len(a.tabs)-1)
			} else if a.cur > i {
				a.cur--
			}
		} else if t.active == p {
			// Focus the first remaining pane in layout order.
			for _, n := range t.root.leaves(nil) {
				if q := t.panes[n.id]; q != nil {
					t.active = q
					break
				}
			}
			if t.active == p {
				for _, q := range t.panes {
					t.active = q
					break
				}
			}
			a.focusChanged(t.active, true)
		}
		break
	}
	if len(a.tabs) > 0 {
		a.relayout()
	}
}

// ---- tab bar -------------------------------------------------------------------

type tabSpan struct{ start, end, index int }

// tabBarLine builds the tab bar as a terminal line, plus the column span
// of each tab for mouse clicks.
func (a *App) tabBarLine(th *theme.Theme) (*term.Line, []tabSpan) {
	cols := a.gridCols
	cells := make([]term.Cell, cols)
	rgb := func(c theme.RGB) term.Color { return term.RGBColor(c.R, c.G, c.B) }
	barBG := rgb(th.Palette[0])
	if th.Palette[0] == th.Background {
		barBG = rgb(blendRGB(th.Background, th.Foreground, 0.08))
	}
	for i := range cells {
		cells[i] = term.Cell{BG: barBG}
	}
	var spans []tabSpan
	x := 0
	maxW := max(8, min(30, cols/max(1, len(a.tabs))))
	for i, t := range a.tabs {
		label := []rune(" " + strconv.Itoa(i+1) + " " + t.title() + " ")
		if len(label) > maxW {
			label = append(label[:maxW-2], '…', ' ')
		}
		fg, bg := rgb(th.Foreground), barBG
		attrs := term.Attr(0)
		if i == a.cur {
			fg, bg = rgb(th.Background), rgb(th.Palette[4])
			attrs = term.AttrBold
		}
		start := x
		for _, r := range label {
			if x >= cols {
				break
			}
			cells[x] = term.Cell{Rune: r, FG: fg, BG: bg, Attrs: attrs}
			x++
		}
		spans = append(spans, tabSpan{start, x, i})
		if x < cols {
			x++ // gap
		}
	}
	return term.NewLine(cells), spans
}

func blendRGB(a, b theme.RGB, t float64) theme.RGB {
	mix := func(x, y uint8) uint8 { return uint8(float64(x)*(1-t) + float64(y)*t) }
	return theme.RGB{R: mix(a.R, b.R), G: mix(a.G, b.G), B: mix(a.B, b.B)}
}

// ---- drawing -------------------------------------------------------------------

// paneOrigin returns the pixel origin and mark gutter of a pane.
func (a *App) paneOrigin(p *pane) (ox, oy, gutter int) {
	cw, ch := a.render.CellSize()
	pad := a.render.Pad()
	ox = pad + p.rect.X*cw
	oy = pad + (p.rect.Y+a.barRows())*ch
	gutter = pad
	if p.rect.X > 0 {
		gutter = cw / 2
	}
	return ox, oy, gutter
}

// separatorRects converts the active tab's separators to pixel rects.
func (a *App) separatorRects(t *tab, color theme.RGB) []render.Rect {
	if t.zoomed != nil {
		return nil
	}
	cw, ch := a.render.CellSize()
	pad := a.render.Pad()
	th := max(1, cw/8)
	bar := a.barRows()
	var out []render.Rect
	for _, s := range t.root.separators(nil) {
		if s.Vertical {
			x := pad + s.X*cw + cw/2 - th/2
			out = append(out, render.Rect{X: x, Y: pad + (s.Y+bar)*ch, W: th, H: s.Len * ch, Color: color})
		} else {
			y := pad + (s.Y+bar)*ch + ch/2 - th/2
			out = append(out, render.Rect{X: pad + s.X*cw, Y: y, W: s.Len * cw, H: th, Color: color})
		}
	}
	return out
}

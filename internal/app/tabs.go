package app

import (
	"fmt"
	"os"
	"strconv"
	"time"

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
	index  string // tmux window index, shown instead of the position
	home   bool   // the home tab of widgets
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
	// In tmux mode the bar is always shown (it doubles as the session's
	// status line), which also keeps the client size stable as windows
	// come and go.
	if (len(a.tabs) > 1 || a.tmux != nil) && a.gridRows > 2 {
		return 1
	}
	return 0
}

// content is the grid area for panes.
func (a *App) content() cellRect {
	return cellRect{0, 0, a.gridCols - a.chatCols(), a.gridRows - a.barRows()}
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
	a.prevTab = a.tab()
	a.cur = i
	a.focusChanged(old, false)
	a.focusChanged(a.active(), true)
	a.forceRedraw = true
	if a.tmux != nil {
		a.tmux.selectWindow(a.tab())
		if a.tmux.remote != nil && a.tmux.follow { // a guest looking around on their own
			a.tmux.follow = false
			a.updateGuestStatus()
		}
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
	if t.home {
		return
	}
	if a.tmux != nil {
		a.tmux.splitPane(t.active, dir, a.activeCwd())
		return
	}
	if p := a.splitLocal(t, t.active, dir, a.newPaneCommand(), a.activeCwd()); p != nil {
		a.setActive(t, p)
	}
}

// splitLocal splits pane cur of tab t, running argv in the new pane. It
// returns nil on failure.
func (a *App) splitLocal(t *tab, cur *pane, dir splitDir, argv []string, cwd string) *pane {
	n := t.root.find(cur.id)
	if n == nil {
		return nil
	}
	// Size the new pane from the split we're about to make.
	w, h := cur.rect.W, cur.rect.H
	if dir == splitH {
		w = cur.rect.W - sizeFor(cur.rect.W, 0.5) - 1
	} else {
		h = cur.rect.H - sizeFor(cur.rect.H, 0.5) - 1
	}
	p, err := a.newLocalPane(argv, cwd, w, h)
	if err != nil {
		fmt.Fprintln(os.Stderr, "germ:", err)
		return nil
	}
	t.zoomed = nil
	t.panes[p.id] = p
	t.root = split(t.root, n, dir, p.id)
	a.relayout()
	return p
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
	if a.tmux != nil && !t.home {
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
	if t := a.tab(); t != nil && t.home {
		return
	}
	if p := a.active(); p != nil {
		p.be.Close()
	}
}

func (a *App) actionCloseTab() {
	if t := a.tab(); t != nil && !t.home {
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
	return a.workTabs() > 0
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
	if a.tmux != nil { // session name first, like tmux's status line
		label, bg := " "+a.tmux.session+" ", th.Palette[2]
		if p := a.tmux.prefixPending; p != "" { // like tmux's client_prefix
			label, bg = " "+p+" ", th.Palette[3]
		}
		for _, r := range label {
			if x < cols {
				cells[x] = term.Cell{Rune: r, FG: rgb(th.Background), BG: rgb(bg), Attrs: term.AttrBold}
				x++
			}
		}
		x++
	}
	maxW := max(8, min(30, cols/max(1, len(a.tabs))))
	now := time.Now()
	for i, t := range a.tabs {
		pos := i + 1
		if a.tabs[0].home {
			pos = i // home is ⌂; work tabs count from 1
		}
		num := strconv.Itoa(pos)
		if t.index != "" {
			num = t.index // match tmux's select-window keys
		}
		head := " " + num + " "
		badge := a.agentBadge(t, now)
		if r := badgeRune(badge, now); r != 0 {
			head += string(r) + " "
		}
		title := t.title()
		if tag := a.helmTag(t); tag != "" {
			title += " " + tag
		}
		label := []rune(head + title + " ")
		if len(label) > maxW {
			label = append(label[:maxW-2], '…', ' ')
		}
		badgeAt := -1
		if badge != stateNone {
			badgeAt = len([]rune(head)) - 2
		}
		badgeFG := map[agentState]theme.RGB{stateWorking: th.Palette[6], stateDone: th.Palette[2],
			stateWaiting: th.Palette[3]}[badge]
		fg, bg := rgb(th.Foreground), barBG
		attrs := term.Attr(0)
		if i == a.cur {
			fg, bg = rgb(th.Background), rgb(th.Palette[4])
			attrs = term.AttrBold
		}
		start := x
		for j, r := range label {
			if x >= cols {
				break
			}
			cells[x] = term.Cell{Rune: r, FG: fg, BG: bg, Attrs: attrs}
			if j == badgeAt && i != a.cur {
				cells[x].FG, cells[x].Attrs = rgb(badgeFG), term.AttrBold
			}
			x++
		}
		spans = append(spans, tabSpan{start, x, i})
		if x < cols {
			x++ // gap
		}
	}
	// Status (sharing, who's here) on the right.
	if st := a.statusLine(); st != "" {
		r := []rune(" " + st + " ")
		start := cols - len(r)
		if start > x+1 {
			for i, ch := range r {
				cells[start+i] = term.Cell{Rune: ch, FG: rgb(th.Background), BG: rgb(th.Palette[5]), Attrs: term.AttrBold}
			}
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

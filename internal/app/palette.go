package app

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/go-gl/glfw/v3.3/glfw"

	"github.com/zach-monroe/germ/internal/config"
	"github.com/zach-monroe/germ/internal/control"
	"github.com/zach-monroe/germ/internal/render"
	"github.com/zach-monroe/germ/internal/term"
	"github.com/zach-monroe/germ/internal/theme"
)

// The command palette (Ctrl+Shift+Space): every action, with its key, plus
// a few commands that have no key, filtered as you type.

type paletteItem struct {
	label string
	keys  string
	run   func(a *App)
	// preview, if set, runs when the item is selected (themes recolor the
	// window); leaving the palette without running it undoes it.
	preview func(a *App)
}

type paletteState struct {
	pane      *pane
	items     []paletteItem
	query     []rune
	filtered  []int
	sel       int
	gen       int
	previewed bool        // a theme preview is showing
	status    *statusText // the tab bar status before it
}

const paletteRows = 12

// actionLabels name actions for people.
var actionLabels = map[config.Action]string{
	config.ActionCopy: "Copy selection", config.ActionPaste: "Paste", config.ActionPastePrimary: "Paste primary selection",
	config.ActionZoomIn: "Bigger text", config.ActionZoomOut: "Smaller text", config.ActionZoomReset: "Reset text size",
	config.ActionScrollPageUp: "Scroll up a page", config.ActionScrollPageDown: "Scroll down a page",
	config.ActionScrollTop: "Scroll to the top", config.ActionScrollBottom: "Scroll to the bottom",
	config.ActionPrevPrompt: "Jump to previous prompt", config.ActionNextPrompt: "Jump to next prompt",
	config.ActionCopyLastOutput: "Copy last command's output",
	config.ActionNewTab:         "New tab", config.ActionCloseTab: "Close tab", config.ActionNextTab: "Next tab", config.ActionPrevTab: "Previous tab",
	config.ActionSplitRight: "Split right", config.ActionSplitDown: "Split down", config.ActionClosePane: "Close pane",
	config.ActionFocusLeft: "Focus pane to the left", config.ActionFocusRight: "Focus pane to the right",
	config.ActionFocusUp: "Focus pane above", config.ActionFocusDown: "Focus pane below",
	config.ActionZoomPane: "Zoom pane (toggle)", config.ActionDetach: "Detach (close window, keep session)",
	config.ActionReloadConfig: "Reload config", config.ActionHome: "Home screen (toggle)",
	config.ActionNewAgent: "New agent on its own branch", config.ActionSendToAgent: "Send selection to agent",
	config.ActionShare: "Share this session (invite link)", config.ActionFollow: "Follow the host (guests)",
	config.ActionSearch: "Search scrollback", config.ActionHints: "Hints: open or copy links, files, hashes",
	config.ActionReview: "Review agents' work",
	config.ActionChat:   "Chat with the session (toggle)", config.ActionHelm: "Helm: take it here (host) or ask for it (guest)",
}

func (a *App) paletteItems() []paletteItem {
	keys := map[config.Action][]string{}
	for ch, act := range a.bindings {
		keys[act] = append(keys[act], chordLabel(ch))
	}
	var items []paletteItem
	// Running agents first, most urgent first: jump to one.
	now := time.Now()
	type agentItem struct {
		it   paletteItem
		rank int
	}
	var agents []agentItem
	for _, p := range a.panes {
		if p.agent.name == "" {
			continue
		}
		st := p.state(now)
		label := "Go to agent: " + p.agent.name
		switch st {
		case stateWaiting:
			label += " · needs you"
		case stateWorking:
			label += " · working"
		case stateDone:
			label += " · done"
		}
		if t := a.tabOf(p); t != nil {
			label += " · " + t.title()
		}
		if p.agent.message != "" {
			label += " · " + truncateRunes(p.agent.message, 40)
		}
		p := p
		agents = append(agents, agentItem{paletteItem{label: label, run: func(a *App) { a.focusPane(p) }}, rank(st)})
	}
	sort.SliceStable(agents, func(i, j int) bool { return agents[i].rank > agents[j].rank })
	for _, ag := range agents {
		items = append(items, ag.it)
	}
	var actionItems []paletteItem
	for act, label := range actionLabels {
		k := keys[act]
		sort.Strings(k)
		if len(k) > 2 {
			k = k[:2]
		}
		act := act
		actionItems = append(actionItems, paletteItem{label: label, keys: strings.Join(k, "  "), run: func(a *App) { a.do(act) }})
	}
	sort.Slice(actionItems, func(i, j int) bool { return actionItems[i].label < actionItems[j].label })
	items = append(items, actionItems...)
	items = append(items, a.chatPaletteItems()...)

	self, _ := os.Executable()
	inTab := func(args ...string) func(a *App) {
		return func(a *App) {
			cmd := shellJoin(append([]string{self}, args...)) + `; echo; printf "Press Enter to close. "; read _`
			noop := func(*control.Response) {}
			cwd := ""
			if p := a.workPane(); p != nil {
				cwd = paneDir(p)
			}
			a.ctlOpen(control.Request{Where: "tab", Command: []string{"/bin/sh", "-c", cmd}, Focus: true, Cwd: cwd}, nil, noop, noop)
		}
	}
	items = append(items,
		paletteItem{label: "Open config file", run: func(a *App) {
			if p := a.active(); p != nil && a.opts.ConfigPath != "" {
				a.openInEditor(p, &fileRef{path: a.opts.ConfigPath})
			}
		}},
		paletteItem{label: "Check setup (germ doctor)", run: inTab("doctor")},
		paletteItem{label: "Set up agents to report their state (germ setup agents)", run: inTab("setup", "agents")},
	)
	items = append(items, a.themeItems()...)
	return items
}

func chordLabel(c config.Chord) string {
	parts := strings.Split(c.String(), "+")
	for i, p := range parts {
		switch p {
		case "grave":
			p = "`"
		case "equal":
			p = "="
		case "minus":
			p = "-"
		}
		if len(p) > 0 {
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
	}
	return strings.Join(parts, "+")
}

// previewTheme recolors this window (until the config is reloaded).
// themeItems are every theme, bundled and your own: selecting one
// previews it, Enter keeps it (in the config), Esc goes back.
func (a *App) themeItems() []paletteItem {
	mine := theme.Installed(config.ThemeDirs()...)
	isMine := map[string]bool{}
	for _, n := range mine {
		isMine[n] = true
	}
	names := append([]string(nil), mine...)
	for _, n := range theme.Bundled() {
		if !isMine[n] {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	var items []paletteItem
	for _, name := range names {
		name := name
		label := "Theme: " + name
		if isMine[name] {
			label += " (yours)"
		}
		if name == a.cfg.Theme {
			label += " · current"
		}
		items = append(items, paletteItem{label: label,
			preview: func(a *App) { a.previewTheme(name) },
			run:     func(a *App) { a.keepTheme(name) }})
	}
	return items
}

// previewTheme recolors the window with a theme, for now.
func (a *App) previewTheme(name string) bool {
	th, err := theme.Load(name, config.ThemeDirs()...)
	if err != nil {
		a.setStatus(err.Error(), 5*time.Second)
		return false
	}
	for _, p := range a.panes {
		p.term.SetTheme(th)
	}
	a.forceRedraw = true
	a.setStatus("theme "+name+" · Enter keeps it, Esc goes back", 0)
	return true
}

// restoreTheme undoes a preview, and puts back the status it covered.
func (a *App) restoreTheme(status *statusText) {
	if th, err := a.cfg.ResolveTheme(); err == nil {
		for _, p := range a.panes {
			p.term.SetTheme(th)
		}
	}
	a.status.Store(status)
	a.forceRedraw = true
}

// keepTheme makes a theme this window's and the config's (every window
// using the config follows when it's saved).
func (a *App) keepTheme(name string) {
	if !a.previewTheme(name) {
		return
	}
	path := a.opts.ConfigPath
	if path == "" {
		path = config.Path()
	}
	old, err := config.SetTheme(path, name)
	if err != nil {
		a.setStatus("couldn't save the theme: "+err.Error(), 8*time.Second)
		return
	}
	msg := fmt.Sprintf("theme %s saved to the config", name)
	if old != "" && old != name {
		msg += " (was " + old + ")"
	}
	a.setStatus(msg, 6*time.Second)
}

// palettePreview previews the selected item, or undoes the last preview.
func (a *App) palettePreview() {
	ps := a.palette
	if ps == nil {
		return
	}
	if len(ps.filtered) > 0 {
		if it := ps.items[ps.filtered[ps.sel]]; it.preview != nil {
			if !ps.previewed {
				ps.status = a.status.Load()
			}
			it.preview(a)
			ps.previewed = true
			return
		}
	}
	if ps.previewed {
		ps.previewed = false
		a.restoreTheme(ps.status)
	}
}

func (a *App) openPalette() bool {
	p := a.active()
	if p == nil {
		return false
	}
	a.closeSearch()
	a.closeHints()
	a.palette = &paletteState{pane: p, items: a.paletteItems(), gen: 1}
	a.filterPalette()
	a.forceRedraw = true
	return true
}

func (a *App) closePalette() {
	if a.palette != nil {
		if a.palette.previewed {
			a.restoreTheme(a.palette.status)
		}
		a.palette = nil
		a.forceRedraw = true
	}
}

// fuzzyScore matches query's letters in order in s (case-insensitively);
// lower is better, -1 is no match. Matches at word starts and runs of
// consecutive letters score better.
func fuzzyScore(s string, query []rune) int {
	if len(query) == 0 {
		return 0
	}
	rs := []rune(strings.ToLower(s))
	score, qi, last := 0, 0, -1
	for i, r := range rs {
		if qi < len(query) && r == unicode.ToLower(query[qi]) {
			switch {
			case last == i-1 && last >= 0:
				// consecutive: free
			case i == 0 || !unicode.IsLetter(rs[i-1]):
				score += 1 // word start
			default:
				score += 3 + min(i-last, 10)
			}
			last = i
			qi++
		}
	}
	if qi < len(query) {
		return -1
	}
	return score
}

func (a *App) filterPalette() {
	ps := a.palette
	type scored struct{ i, s int }
	var out []scored
	for i, it := range ps.items {
		if s := fuzzyScore(it.label, ps.query); s >= 0 {
			out = append(out, scored{i, s})
		}
	}
	sort.SliceStable(out, func(x, y int) bool { return out[x].s < out[y].s })
	ps.filtered = ps.filtered[:0]
	for _, o := range out {
		ps.filtered = append(ps.filtered, o.i)
	}
	ps.sel = 0
	ps.gen++
	a.forceRedraw = true
	a.palettePreview()
}

func (a *App) paletteKey(key glfw.Key, mods glfw.ModifierKey, action glfw.Action) {
	if action == glfw.Release {
		return
	}
	ps := a.palette
	ctrl := mods&glfw.ModControl != 0
	move := func(d int) {
		if n := len(ps.filtered); n > 0 {
			ps.sel = (ps.sel + d + n) % n
			ps.gen++
			a.forceRedraw = true
			a.palettePreview()
		}
	}
	switch {
	case key == glfw.KeyEscape:
		a.closePalette()
	case key == glfw.KeyEnter || key == glfw.KeyKPEnter:
		if len(ps.filtered) == 0 {
			return
		}
		it := ps.items[ps.filtered[ps.sel]]
		if it.preview != nil {
			ps.previewed = false // kept, not undone
		}
		a.closePalette()
		it.run(a)
	case key == glfw.KeyUp || ctrl && (key == glfw.KeyP || key == glfw.KeyK):
		move(-1)
	case key == glfw.KeyDown || ctrl && (key == glfw.KeyN || key == glfw.KeyJ) || key == glfw.KeyTab:
		move(1)
	case key == glfw.KeyBackspace && len(ps.query) > 0:
		if ctrl {
			ps.query = ps.query[:0]
		} else {
			ps.query = ps.query[:len(ps.query)-1]
		}
		a.filterPalette()
	case ctrl && key == glfw.KeyU:
		ps.query = ps.query[:0]
		a.filterPalette()
	case ctrl && mods&glfw.ModShift != 0 && key == glfw.KeySpace:
		a.closePalette() // the shortcut again toggles it
	}
}

func (a *App) paletteType(r rune) {
	if unicode.IsPrint(r) {
		a.palette.query = append(a.palette.query, r)
		a.filterPalette()
	}
}

// paletteView draws the palette over the top rows of its pane.
func (a *App) paletteView(p *pane, v *render.View) {
	ps := a.palette
	if ps == nil || ps.pane != p {
		return
	}
	cols, rows := p.term.Size()
	th := p.surf.Theme()
	rgb := func(c theme.RGB) term.Color { return term.RGBColor(c.R, c.G, c.B) }
	bg := blendRGB(th.Background, th.Foreground, 0.10)
	selBG := blendRGB(th.Background, th.Palette[4], 0.35)
	fg, dim, accent := rgb(th.Foreground), rgb(th.Palette[8]), rgb(th.Palette[4])
	line := func(back theme.RGB, left []struct {
		text  string
		fg    term.Color
		attrs term.Attr
	}, right string, rightFG term.Color) *term.Line {
		cells := make([]term.Cell, cols)
		for i := range cells {
			cells[i] = term.Cell{BG: rgb(back)}
		}
		x := 0
		for _, seg := range left {
			for _, r := range seg.text {
				if x < cols {
					cells[x] = term.Cell{Rune: r, FG: seg.fg, BG: rgb(back), Attrs: seg.attrs}
					x++
				}
			}
		}
		if rr := []rune(right); len(rr) > 0 && cols-len(rr)-1 > x+1 {
			for i, r := range rr {
				cells[cols-len(rr)-1+i] = term.Cell{Rune: r, FG: rightFG, BG: rgb(back)}
			}
		}
		return term.NewLine(cells)
	}
	type seg = struct {
		text  string
		fg    term.Color
		attrs term.Attr
	}
	n := min(len(ps.filtered), paletteRows, max(0, rows-2))
	hint := fmt.Sprintf("%d · ↑↓ Enter · Esc", len(ps.filtered))
	if ps.previewed {
		hint = "previewing · Enter keeps it · Esc goes back"
	}
	lines := []*term.Line{line(bg, []seg{{" ❯ ", accent, term.AttrBold}, {string(ps.query), fg, 0}, {"▏", accent, 0}},
		hint, dim)}
	// Keep the selection in view.
	first := 0
	if ps.sel >= n {
		first = ps.sel - n + 1
	}
	for i := first; i < first+n; i++ {
		it := ps.items[ps.filtered[i]]
		back, mark, keyFG := bg, "   ", dim
		if i == ps.sel {
			back, mark, keyFG = selBG, " ▸ ", fg // dim can vanish on the selection color
		}
		lines = append(lines, line(back, []seg{{mark, accent, 0}, {it.label, fg, 0}}, it.keys, keyFG))
	}
	if len(ps.filtered) == 0 {
		lines = append(lines, line(bg, []seg{{"   no matching command", dim, 0}}, "", dim))
	}
	v.TopOverlay = lines
	v.TopOverlayGen = ps.gen
}

func truncateRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// tabOf finds the tab holding p.
func (a *App) tabOf(p *pane) *tab {
	for _, t := range a.tabs {
		if t.panes[p.id] != nil {
			return t
		}
	}
	return nil
}

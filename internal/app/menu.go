package app

import (
	"fmt"
	"strings"
	"time"

	"github.com/go-gl/glfw/v3.3/glfw"

	"github.com/zach-monroe/germ/internal/config"
	"github.com/zach-monroe/germ/internal/share"
)

// Right-click menus: what you can do with the pane, the tab, or the people
// in a shared session where you clicked. They're the palette, filled with
// those items and titled, over the top of the pane (clickable).

// openMenu shows items as a menu over p.
func (a *App) openMenu(p *pane, title string, items []paletteItem) {
	if p == nil || len(items) == 0 {
		return
	}
	a.closePalette()
	a.closeSearch()
	a.closeHints()
	a.palette = &paletteState{pane: p, items: items, title: title, gen: 1}
	a.filterPalette()
	a.forceRedraw = true
}

// openPrompt asks for a line of text over p, starting with initial.
func (a *App) openPrompt(p *pane, title, initial string, fn func(a *App, text string)) {
	if p == nil {
		return
	}
	a.closePalette()
	a.palette = &paletteState{pane: p, title: title, prompt: fn, query: []rune(initial), gen: 1}
	a.filterPalette()
}

// menuClick handles a click while a menu (or the palette) is open: on an
// item, it runs it; anywhere else, it closes the menu. It reports whether
// it used the click.
func (a *App) menuClick() bool {
	ps := a.palette
	if ps == nil {
		return false
	}
	p, _, row, inBar := a.hit()
	if !inBar && p == ps.pane && row >= 1 && row <= ps.shown {
		if i := ps.first + row - 1; i < len(ps.filtered) {
			it := ps.items[ps.filtered[i]]
			if it.preview != nil {
				ps.previewed = false
			}
			a.closePalette()
			it.run(a)
			return true
		}
	}
	if !inBar && p == ps.pane && row == 0 {
		return true // the query line
	}
	a.closePalette()
	return true
}

// rightClick opens the menu for what's under the mouse. It reports false
// if there's nothing to show (the click goes on as usual).
func (a *App) rightClick(mods glfw.ModifierKey) bool {
	if row, ok := a.chatHit(a.mouse.gx, a.mouse.gy); ok {
		return a.chatMenu(row)
	}
	p, _, _, inBar := a.hit()
	if inBar {
		for _, s := range a.tabSpans {
			if a.mouse.gx >= s.start && a.mouse.gx < s.end {
				a.switchTab(s.index)
				a.tabMenu()
				return true
			}
		}
		return false
	}
	if p == nil {
		return false
	}
	if _, _, rep := reporting(p, mods); rep {
		return false // the program wants the mouse (Shift+right-click for the menu)
	}
	if t := a.tab(); t != nil && p != t.active {
		a.setActive(t, p)
	}
	a.paneMenu(p)
	return true
}

// act runs a germ action as a menu item.
func act(label string, action config.Action) paletteItem {
	return paletteItem{label: label, run: func(a *App) { a.do(action) }}
}

// paneMenu is what you can do with p (the active pane) and its tab.
func (a *App) paneMenu(p *pane) {
	var items []paletteItem
	if p.sel.active {
		items = append(items, act("Copy", config.ActionCopy))
	}
	items = append(items, act("Paste", config.ActionPaste))
	if u := p.linkURL; u != "" {
		items = append(items,
			paletteItem{label: "Open link", run: func(*App) { openURL(u) }},
			paletteItem{label: "Copy link", run: func(*App) { glfw.SetClipboardString(u) }})
	}
	items = append(items, a.helmItems(a.tab())...)
	_, widget := p.be.(*widgetBackend)
	if t := a.tab(); t != nil && !t.home && !widget {
		zoom := "Zoom pane"
		if t.zoomed != nil {
			zoom = "Unzoom pane"
		}
		items = append(items,
			act("Split right", config.ActionSplitRight),
			act("Split down", config.ActionSplitDown),
			act(zoom, config.ActionZoomPane))
		if b, ok := p.be.(*tmuxBackend); ok && b.c.remote == nil {
			items = append(items, paletteItem{label: "Restart pane (kill what runs in it, start a new shell)", run: func(a *App) {
				b.c.command(fmt.Sprintf("respawn-pane -k -t %%%d", b.id), nil)
			}})
		}
		items = append(items, act("Close pane", config.ActionClosePane))
	}
	items = append(items, act("New tab", config.ActionNewTab), act("Search scrollback", config.ActionSearch),
		act("Command palette", config.ActionPalette))
	a.openMenu(p, "pane", items)
}

// tabMenu is what you can do with the current tab.
func (a *App) tabMenu() {
	t := a.tab()
	if t == nil || t.active == nil {
		return
	}
	p := t.active // drawn over the top-left pane, by the tab bar
	if ls := t.root.leaves(nil); len(ls) > 0 && t.panes[ls[0].id] != nil {
		p = t.panes[ls[0].id]
	}
	var items []paletteItem
	if !t.home {
		items = append(items, paletteItem{label: "Rename tab…", run: func(a *App) {
			a.openPrompt(p, "rename tab", t.title(), func(a *App, name string) {
				if name = strings.TrimSpace(name); name != "" {
					a.renameTab(p, name)
				}
			})
		}})
	}
	items = append(items, act("New tab", config.ActionNewTab))
	if !t.home {
		if a.cur > 0 && !a.tabs[a.cur-1].home {
			items = append(items, paletteItem{label: "Move tab left", run: func(a *App) { a.moveTab(-1) }})
		}
		if a.cur < len(a.tabs)-1 {
			items = append(items, paletteItem{label: "Move tab right", run: func(a *App) { a.moveTab(1) }})
		}
		items = append(items, a.helmItems(t)...)
		items = append(items, a.giveItems(t)...)
		if a.tmux != nil && a.tmux.remote == nil && t.tmuxID != "" {
			items = append(items, paletteItem{label: "Restart every pane in the tab", run: func(a *App) {
				for _, q := range t.panes {
					if b, ok := q.be.(*tmuxBackend); ok {
						b.c.command(fmt.Sprintf("respawn-pane -k -t %%%d", b.id), nil)
					}
				}
			}})
		}
		items = append(items, act("Close tab (and everything running in it)", config.ActionCloseTab))
	}
	a.openMenu(p, "tab", items)
}

// moveTab swaps the current tab with its neighbour (d: -1 left, +1 right).
func (a *App) moveTab(d int) {
	j := a.cur + d
	if j < 0 || j >= len(a.tabs) || a.tabs[j].home || a.tab().home {
		return
	}
	t, u := a.tab(), a.tabs[j]
	if a.tmux != nil && t.tmuxID != "" && u.tmuxID != "" {
		a.tmux.command("swap-window -d -s "+t.tmuxID+" -t "+u.tmuxID, nil)
		a.tmux.command("select-window -t "+t.tmuxID, nil)
		return // tmux reorders the tabs
	}
	a.tabs[a.cur], a.tabs[j] = u, t
	a.cur = j
	a.forceRedraw = true
}

// helmItems are the helm of tab t, while sharing: hand it out and take it
// back (host), ask for it or hand it back (guests).
func (a *App) helmItems(t *tab) []paletteItem {
	c := a.chat
	if !c.active() || t == nil || t.tmuxID == "" {
		return nil
	}
	win := t.tmuxID
	holder := c.state.HelmOf(win)
	var items []paletteItem
	if c.host {
		if holder != 0 {
			items = append(items, paletteItem{label: "Take the helm back from " + shortName(a.personName(holder)),
				run: func(a *App) { a.chatAct(share.Action{Op: share.OpTake, Window: win}) }})
		}
		for _, p := range c.people {
			if p.Host || p.ID == holder {
				continue
			}
			id := p.ID
			items = append(items, paletteItem{label: "Hand " + shortName(p.Name) + " the helm here",
				run: func(a *App) { a.chatAct(share.Action{Op: share.OpHand, Window: win, Person: id}) }})
		}
		return items
	}
	label := "Ask for the helm here"
	if holder == c.you {
		label = "Hand back the helm"
	}
	return []paletteItem{{label: label, run: func(a *App) { a.chatToast(a.helmAct(win), 4*time.Second) }}}
}

// giveItems give tab t to a guest, or take it back (host, sharing).
func (a *App) giveItems(t *tab) []paletteItem {
	c := a.chat
	if !c.active() || !c.host || t == nil || t.tmuxID == "" {
		return nil
	}
	win := t.tmuxID
	var items []paletteItem
	st := c.state.Tab(win)
	if st != nil && (st.Owner != 0 || st.Helm != 0) {
		items = append(items, paletteItem{label: "Take this tab back from " + shortName(a.personName(max(st.Owner, st.Helm))),
			run: func(a *App) { a.chatAct(share.Action{Op: share.OpUntake, Window: win}) }})
	}
	for _, p := range c.people {
		if p.Host || (st != nil && st.Owner == p.ID) {
			continue
		}
		id := p.ID
		items = append(items, paletteItem{label: "Give this tab to " + shortName(p.Name),
			run: func(a *App) { a.chatAct(share.Action{Op: share.OpGive, Window: win, Person: id}) }})
	}
	return items
}

// chatMenu is the people and the session, for a right-click in the chat
// panel. On a person's name, just that person.
func (a *App) chatMenu(row int) bool {
	c := a.chat
	p := a.active()
	if p == nil || !c.active() {
		return false
	}
	var items []paletteItem
	if label, act, ok := a.chatDecision(); ok {
		yes, no := "Yes: ", "No: "
		items = append(items,
			paletteItem{label: yes + label, run: func(a *App) { act.Allow = true; a.chatAct(act); a.chatDecided(act) }},
			paletteItem{label: no + label, run: func(a *App) { act.Allow = false; a.chatAct(act); a.chatDecided(act) }})
	}
	clicked := a.chatWordAt(row)
	t := a.tab()
	if c.host {
		for _, pp := range c.people {
			if pp.Host || (clicked != "" && !share.NameMatches(pp.Name, clicked)) {
				continue
			}
			items = append(items, a.personItems(pp, t)...)
		}
		if clicked == "" || len(items) == 0 {
			link := c.link
			items = append(items,
				paletteItem{label: "Copy the invite link", run: func(a *App) {
					glfw.SetClipboardString(link)
					a.chatToast("copied the invite link", 4*time.Second)
				}},
				paletteItem{label: "New invite link (the old one stops working)", run: func(a *App) { a.chatCommand("/newlink") }},
				paletteItem{label: "Save the chat to a file", run: func(a *App) { a.chatAct(share.Action{Op: share.OpSave}) }},
				paletteItem{label: "Stop sharing", run: func(a *App) { a.chatAct(share.Action{Op: share.OpStop}) }})
		}
	} else {
		items = append(items, a.helmItems(t)...)
		items = append(items, act("Follow the host", config.ActionFollow))
	}
	a.openMenu(p, "session", items)
	return true
}

// personItems are what the host can do with a guest.
func (a *App) personItems(pp share.Participant, t *tab) []paletteItem {
	c := a.chat
	id, name := pp.ID, shortName(pp.Name)
	var items []paletteItem
	if t != nil && t.tmuxID != "" {
		win, tabName := t.tmuxID, a.chatTabName(t.tmuxID)
		if c.state.HelmOf(win) == id {
			items = append(items, paletteItem{label: "Take the helm of " + tabName + " back from " + name,
				run: func(a *App) { a.chatAct(share.Action{Op: share.OpTake, Window: win}) }})
		} else {
			items = append(items, paletteItem{label: "Hand " + name + " the helm of " + tabName,
				run: func(a *App) { a.chatAct(share.Action{Op: share.OpHand, Window: win, Person: id}) }})
		}
		items = append(items, paletteItem{label: "Give " + tabName + " to " + name,
			run: func(a *App) { a.chatAct(share.Action{Op: share.OpGive, Window: win, Person: id}) }})
	}
	mute, op := "Mute ", share.OpMute
	if pp.Muted {
		mute, op = "Unmute ", share.OpUnmute
	}
	items = append(items,
		paletteItem{label: "New tab for " + name + " on its own branch", run: func(a *App) { a.branchFor(name, "") }},
		paletteItem{label: mute + name + " in chat", run: func(a *App) { a.chatAct(share.Action{Op: op, Person: id}) }},
		paletteItem{label: "Remove " + name + " from the session", run: func(a *App) { a.chatAct(share.Action{Op: share.OpKick, Person: id}) }})
	return items
}

// chatWordAt is the word under the mouse in panel row row ("" if none).
func (a *App) chatWordAt(row int) string {
	c := a.chat
	if row < 0 || row >= len(c.rowLinks) {
		return ""
	}
	text := []rune(c.rowLinks[row])
	col := a.mouse.gx - (a.gridCols - a.chatCols() + 1)
	if col < 0 || col >= len(text) || text[col] == ' ' {
		return ""
	}
	start, end := col, col
	for start > 0 && text[start-1] != ' ' {
		start--
	}
	for end < len(text) && text[end] != ' ' {
		end++
	}
	return strings.Trim(string(text[start:end]), "·:,★✎()")
}

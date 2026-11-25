package chatview

import (
	"fmt"

	"github.com/mattn/go-runewidth"

	"github.com/zach-monroe/germ/internal/share"
)

// HostHead is the host's header above the chat: the session, the invite
// link (in full, wrapped: cut, it wouldn't work when copied), who's here
// (numbered, for commands) and who has the helm of each tab (the host's
// current tab underlined: commands act on it by default). Rows are cols
// wide at most.
func HostHead(session, via, link string, people []share.Participant, st share.State, cols int) []string {
	const b, d, r = "\x1b[1m", "\x1b[2m", "\x1b[0m"
	rows := []string{fmt.Sprintf("%s\x1b[34m◆ contagious%s%s · %s %s", b, r, d, session, via)}
	l := []rune(link)
	for len(l) > 0 && cols > 0 {
		n := min(len(l), cols)
		rows = append(rows, "\x1b[36m"+string(l[:n]))
		l = l[n:]
	}
	names := map[uint32]string{0: "you"}
	var here []item
	for i, p := range people {
		if p.Host {
			continue
		}
		name := share.ShortName(p.Name)
		names[p.ID] = name
		s, w := fmt.Sprintf("%s%d%s %s", b, i, r, name), fmt.Sprintf("%d %s", i, name)
		for _, tag := range []struct {
			on   bool
			text string
		}{{p.Web, " (browser)"}, {p.Typing, " ✎"}, {p.Muted, " (muted)"}} {
			if tag.on {
				s, w = s+d+tag.text+r, w+tag.text
			}
		}
		here = append(here, item{s, runewidth.StringWidth(w)})
	}
	if len(here) == 0 {
		rows = append(rows, d+"nobody's here yet: send them the link")
	} else {
		rows = append(rows, wrapItems("here: ", here, "  ", cols)...)
	}
	var tabs []item
	for _, t := range st.Tabs {
		who := names[t.Helm]
		if who == "" {
			who = "someone"
		}
		plain := fmt.Sprintf("%d %s: %s", t.Index, t.Name, who)
		s := fmt.Sprintf("%s%d%s %s: ", b, t.Index, r, t.Name)
		if t.Helm == 0 {
			s += d + "you" + r
		} else {
			s += "\x1b[33m" + who + r
		}
		if t.Owner != 0 {
			s += d + " (" + names[t.Owner] + "'s tab)" + r
			plain += " (" + names[t.Owner] + "'s tab)"
		}
		if t.Window == st.Current {
			s = "\x1b[4m" + s + r
		}
		tabs = append(tabs, item{s, runewidth.StringWidth(plain)})
	}
	if len(tabs) > 0 {
		rows = append(rows, wrapItems("⎈ ", tabs, d+" · "+r, cols)...)
	}
	for i := range rows {
		rows[i] = FitStyled(rows[i], cols)
	}
	return rows
}

// FitStyled cuts s, which may hold SGR escapes, to cols visible columns.
func FitStyled(s string, cols int) string {
	if runewidth.StringWidth(stripStyle(s)) <= cols {
		return s
	}
	out := []rune{}
	w, in := 0, false
	for _, r := range s {
		switch {
		case r == '\x1b':
			in = true
			out = append(out, r)
			continue
		case in:
			out = append(out, r)
			if r == 'm' {
				in = false
			}
			continue
		}
		rw := runewidth.RuneWidth(r)
		if w+rw > cols-1 {
			break
		}
		out = append(out, r)
		w += rw
	}
	return string(out) + "…"
}

type item struct {
	styled string
	width  int
}

// wrapItems lays items out after lead, sep between them, on as many rows
// as they need (an item too wide for a row is cut).
func wrapItems(lead string, items []item, sep string, cols int) []string {
	sepW := runewidth.StringWidth(stripStyle(sep))
	indent := runewidth.StringWidth(lead)
	var rows []string
	cur, w := lead, indent
	for i, it := range items {
		if i > 0 && w+sepW+it.width > cols {
			rows = append(rows, cur)
			cur, w = fmt.Sprintf("%*s", indent, ""), indent
		} else if i > 0 {
			cur, w = cur+sep, w+sepW
		}
		cur, w = cur+it.styled, w+it.width
	}
	return append(rows, cur)
}

// stripStyle removes SGR escapes ("\x1b[...m").
func stripStyle(s string) string {
	out := []rune{}
	in := false
	for _, r := range s {
		switch {
		case r == '\x1b':
			in = true
		case in && r == 'm':
			in = false
		case !in:
			out = append(out, r)
		}
	}
	return string(out)
}

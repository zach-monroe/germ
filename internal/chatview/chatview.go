// Package chatview draws a shared session's chat as terminal output: the
// panel in germ's window and the host's share pane both use it.
package chatview

import (
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/mattn/go-runewidth"

	"github.com/zach-monroe/germ/internal/share"
)

const groupGap = 5 * time.Minute // a new header after this long

// View is what Render draws.
type View struct {
	Msgs     []share.ChatMsg
	People   []share.Participant
	You      uint32
	Host     bool
	Me       string // our short name, for mentions
	Input    []rune
	Cur      int
	Focused  bool
	Scroll   int
	Divider  uint64
	Decision string // a request we can answer
	MoreAsks int
	Typing   []string // people composing
	MyHelm   string   // what we hold, e.g. "⎈ you have the helm of tab 2"
	// Head replaces the "chat · N here" header with these rows (styled;
	// the share pane's link, people and tabs).
	Head []string
	// Notice shows on the status row, before typing and MyHelm (e.g.
	// "copied the invite link").
	Notice string
	// Hint is shown in the empty message box, instead of the default.
	Hint string
}

// Render draws the panel as terminal output, and returns each row's
// plain text (for clicking links).
func Render(v View, cols, rows int) (string, []string) {
	var b strings.Builder
	plain := make([]string, rows)
	b.WriteString("\x1b[?25l\x1b[0m\x1b[H\x1b[2J")
	row := func(r int, styled, text string) {
		if r < 0 || r >= rows {
			return
		}
		fmt.Fprintf(&b, "\x1b[%d;1H%s\x1b[0m", r+1, styled)
		plain[r] = text
	}
	rule := "\x1b[2m" + strings.Repeat("─", cols)

	// Header: who's here.
	var names []string
	for _, p := range v.People {
		n := share.ShortName(p.Name)
		if p.Host {
			n += "★"
		}
		names = append(names, n)
	}
	top := 1 // rows above the rule
	if len(v.Head) > 0 {
		for i, h := range v.Head {
			row(i, h, stripStyle(h))
		}
		top = len(v.Head)
	} else {
		head := fmt.Sprintf(" chat · %d here", len(v.People))
		row(0, "\x1b[1m"+Fit(head, cols)+"\x1b[0;2m"+Fit(" "+strings.Join(names, " "), cols-runewidth.StringWidth(head)), head)
	}
	row(top, rule, "")
	top++

	// Footer, bottom up: the message box, a rule, typing / your helm, and
	// a request to answer.
	inLines, curRow, curCol := WrapInput(v.Input, v.Cur, cols-2)
	if len(inLines) > 5 {
		skip := max(0, min(curRow-4, len(inLines)-5))
		inLines, curRow = inLines[skip:skip+5], curRow-skip
	}
	wide := cols >= 60 // a request and its keys fit on one row
	footer := len(inLines) + 2
	if v.Decision != "" {
		footer += 2
		if wide {
			footer--
		}
	}
	y := rows - len(inLines)
	for i, l := range inLines {
		prompt := "  "
		if i == 0 {
			prompt = "\x1b[1;34m›\x1b[0m "
		}
		if len(v.Input) == 0 {
			hint := "Ctrl+Shift+M to chat"
			switch {
			case v.Hint != "":
				hint = v.Hint
			case v.Focused:
				hint = "message · Enter sends · Esc back"
			}
			row(y+i, prompt+"\x1b[2m"+Fit(hint, cols-2), "")
		} else {
			row(y+i, prompt+l, "  "+l)
		}
	}
	status := ""
	switch {
	case v.Notice != "":
		status = "\x1b[32m" + Fit(v.Notice, cols)
	case len(v.Typing) == 1:
		status = "\x1b[2;3m" + Fit(v.Typing[0]+" is typing…", cols)
	case len(v.Typing) > 1:
		status = "\x1b[2;3m" + Fit(strings.Join(v.Typing, ", ")+" are typing…", cols)
	case v.MyHelm != "":
		status = "\x1b[32m" + Fit(v.MyHelm, cols)
	}
	row(y-1, rule, "")
	row(y-2, status, "")
	if v.Decision != "" {
		more := ""
		if v.MoreAsks > 0 {
			more = fmt.Sprintf(" (+%d)", v.MoreAsks)
		}
		keys := "  y yes · n no"
		if len(v.Input) > 0 || !v.Focused {
			keys = "  /yes or /no (or y/n, box empty)"
		}
		ask := "» " + v.Decision + more
		if wide {
			row(y-3, "\x1b[1;33m"+Fit(ask, cols)+"\x1b[0;33m"+Fit(keys, cols-runewidth.StringWidth(ask)), "")
		} else {
			row(y-4, "\x1b[1;33m"+Fit(ask, cols), "")
			row(y-3, "\x1b[33m"+Fit(keys, cols), "")
		}
	}

	// Messages fill the middle, newest at the bottom.
	area := rows - top - footer
	lines := msgLines(v, cols)
	maxScroll := max(0, len(lines)-area)
	scroll := min(v.Scroll, maxScroll)
	start := max(0, len(lines)-area-scroll)
	shown := lines[start:min(len(lines), max(start, start+area))]
	first := top
	if len(shown) < area {
		first += area - len(shown) // stick to the bottom
	}
	for i, l := range shown {
		row(first+i, l.styled, l.plain)
	}
	if scroll > 0 {
		row(rows-footer-1, "\x1b[7m"+Fit(fmt.Sprintf(" ↓ %d more · PgDn ", scroll), cols), "")
	}
	if v.Focused {
		fmt.Fprintf(&b, "\x1b[%d;%dH\x1b[?25h", y+curRow+1, curCol+3)
	}
	return b.String(), plain
}

type line struct{ styled, plain string }

// nameColors are palette entries for people's names (by ID).
var nameColors = []int{4, 5, 6, 2, 1}

// msgLines lays out the messages: a header per run of one person's
// messages, text indented under it, events dim.
func msgLines(v View, cols int) []line {
	var out []line
	var prev *share.ChatMsg
	dividerDone := v.Divider == 0
	for i := range v.Msgs {
		m := &v.Msgs[i]
		if !dividerDone && m.Seq > v.Divider {
			dividerDone = true
			label := " new "
			side := max(0, (cols-len(label))/2)
			out = append(out, line{"\x1b[31m" + strings.Repeat("─", side) + label + strings.Repeat("─", max(0, cols-side-len(label))), ""})
			prev = nil
		}
		at := time.UnixMilli(m.At)
		if m.Event {
			for j, l := range WrapText(m.Text, cols-2) {
				lead := "· "
				if j > 0 {
					lead = "  "
				}
				out = append(out, line{"\x1b[2m" + lead + l, lead + l})
			}
			prev = nil
			continue
		}
		if prev == nil || prev.From != m.From || prev.Host != m.Host || at.Sub(time.UnixMilli(prev.At)) > groupGap {
			name := share.ShortName(m.Name)
			color := "\x1b[1;3" + fmt.Sprint(nameColors[int(m.From)%len(nameColors)]) + "m"
			if m.Host {
				name += " ★"
				color = "\x1b[1;33m"
			}
			when := " · " + at.Format("15:04")
			out = append(out, line{color + Fit(name, cols-len(when)) + "\x1b[0;2m" + when, name + when})
		}
		mention := mentions(m.Text, v.Me) && !(m.From == v.You && m.Host == v.Host)
		for _, l := range WrapText(m.Text, cols-2) {
			lead := "  "
			if mention {
				lead = "\x1b[33m▌\x1b[0m "
			}
			out = append(out, line{lead + l, "  " + l})
		}
		prev = m
	}
	return out
}

func mentions(text, me string) bool {
	t := strings.ToLower(text)
	if strings.Contains(t, "@here") {
		return true
	}
	return me != "" && strings.Contains(t, "@"+strings.ToLower(me))
}

// WrapText wraps text to width columns, breaking at spaces where it can.
// Control characters become spaces.
func WrapText(text string, width int) []string {
	width = max(width, 4)
	var out []string
	for _, para := range strings.Split(text, "\n") {
		para = strings.Map(func(r rune) rune {
			if unicode.IsControl(r) {
				return ' '
			}
			return r
		}, para)
		line, w := []rune{}, 0
		lastSpace := -1
		for _, r := range para {
			rw := runewidth.RuneWidth(r)
			if w+rw > width {
				if lastSpace > 0 {
					out = append(out, string(line[:lastSpace]))
					line = append([]rune(nil), line[lastSpace+1:]...)
				} else {
					out = append(out, string(line))
					line = nil
				}
				w = runewidth.StringWidth(string(line))
				lastSpace = -1
			}
			if r == ' ' {
				lastSpace = len(line)
			}
			line = append(line, r)
			w += rw
		}
		out = append(out, string(line))
	}
	return out
}

// WrapInput wraps the message box, returning where the cursor is.
func WrapInput(in []rune, cur, width int) (lines []string, curRow, curCol int) {
	width = max(width, 4)
	line, w := []rune{}, 0
	for i, r := range in {
		if i == cur {
			curRow, curCol = len(lines), w
		}
		if r == '\n' {
			lines = append(lines, string(line))
			line, w = nil, 0
			continue
		}
		rw := runewidth.RuneWidth(r)
		if w+rw > width {
			lines = append(lines, string(line))
			line, w = nil, 0
			if i == cur {
				curRow, curCol = len(lines), 0
			}
		}
		line = append(line, r)
		w += rw
	}
	if cur >= len(in) {
		curRow, curCol = len(lines), w
		if w >= width {
			lines = append(lines, string(line))
			line, curRow, curCol = nil, curRow+1, 0
		}
	}
	lines = append(lines, string(line))
	return lines, curRow, curCol
}

// fit truncates s to width columns.
func Fit(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if runewidth.StringWidth(s) <= width {
		return s
	}
	return runewidth.Truncate(s, width, "…")
}

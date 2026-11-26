package widgets

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/zach-monroe/germ/internal/control"
	"github.com/zach-monroe/germ/internal/share"
)

// coworking shows the sessions your germ windows share or have joined:
// who's there, who has the helm of which tab, who's waiting to join or
// asking for the helm, and the chat's unread messages.
type coworking struct {
	status func() []control.ShareStatus // for tests

	mu       sync.Mutex
	sessions []control.ShareStatus
	ready    bool
}

func newCoworking(Options) Widget { return &coworking{status: shareStatuses} }

func (c *coworking) Every() time.Duration { return 2 * time.Second }
func (c *coworking) Tick() time.Duration  { return 0 }

// shareStatuses asks every germ window about its shared session.
func shareStatuses() []control.ShareStatus {
	var out []control.ShareStatus
	seen := map[string]bool{}
	for _, sock := range control.Sockets() {
		cl, err := control.Dial(sock)
		if err != nil {
			continue
		}
		resp, err := cl.Call(control.Request{Cmd: control.CmdShareStatus})
		cl.Close()
		if err != nil || resp.ShareStatus == nil {
			continue
		}
		// Two windows on one session tell the same story.
		k := strconv.FormatBool(resp.ShareStatus.Host) + "/" + resp.ShareStatus.Session
		if !seen[k] {
			seen[k] = true
			out = append(out, *resp.ShareStatus)
		}
	}
	return out
}

func (c *coworking) Update(context.Context) error {
	s := c.status()
	sort.SliceStable(s, func(i, j int) bool { return s[i].Host && !s[j].Host })
	c.mu.Lock()
	c.sessions, c.ready = s, true
	c.mu.Unlock()
	return nil
}

func (c *coworking) Draw(w, h int, err error) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	guests, waiting := 0, 0
	for _, s := range c.sessions {
		for _, p := range s.People {
			if !p.Host && p.ID != s.You {
				guests++
			}
		}
		if s.Host {
			waiting += len(s.Joins)
		}
		waiting += len(s.Requests)
	}
	info := ""
	switch {
	case waiting > 0:
		info = fmt.Sprintf("%d waiting on you", waiting)
	case len(c.sessions) > 0:
		info = plural(guests, "guest")
	}
	lines := []string{title(w, "◉", "coworking", info)}
	if !c.ready {
		return fill(lines, h, errLine(err, w))
	}
	if len(c.sessions) == 0 {
		lines = append(lines, "", dim+"  Not sharing."+reset)
		if h > 4 {
			lines = append(lines, "", dim+"  Ctrl+Shift+P shares this window's"+reset,
				dim+"  session; who joins shows up here."+reset)
		}
		return fill(lines, h, errLine(err, w))
	}
	var body []string
	for _, s := range c.sessions {
		body = append(body, "")
		body = append(body, sessionLines(s, w)...)
	}
	limit := h - 1 - boolInt(err != nil)
	if len(body) > limit { // short on room: no blank separators
		var packed []string
		for _, l := range body {
			if l != "" {
				packed = append(packed, l)
			}
		}
		body = packed
	}
	if len(body) > limit {
		body = body[:max(0, limit)]
	}
	lines = append(lines, body...)
	return fill(lines, h, errLine(err, w))
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// sessionLines draws one shared session: a heading, its people, then
// anything waiting for an answer and the chat.
func sessionLines(s control.ShareStatus, w int) []string {
	var host string
	for _, p := range s.People {
		if p.Host {
			host = shortName(p.Name)
		}
	}
	head := "sharing " + bold + s.Session + reset
	if !s.Host {
		head = "joined " + bold + host + reset + dim + "'s " + reset + bold + s.Session + reset
	}
	if s.Via != "" && w >= 50 {
		head += dim + " · " + s.Via + reset
	}
	out := []string{" " + dim + truncateStyled(head, w-2) + reset}

	tabName := func(win string) string {
		if t := (share.State{Tabs: s.Tabs}).Tab(win); t != nil {
			return fmt.Sprintf("%d %s", t.Index, t.Name)
		}
		return win
	}
	names := map[uint32]string{}
	people := append([]share.Participant(nil), s.People...)
	sort.SliceStable(people, func(i, j int) bool { return people[i].Host && !people[j].Host })
	for _, p := range people {
		names[p.ID] = shortName(p.Name)
		dot := green + "●" + reset
		if p.Muted {
			dot = dim + "●" + reset
		}
		name := shortName(p.Name)
		var tags []string
		if p.ID == s.You && (p.Host == s.Host) {
			tags = append(tags, dim+"you"+reset)
		}
		if p.Host {
			tags = append(tags, dim+"host"+reset)
		}
		if p.Web {
			tags = append(tags, dim+"browser"+reset)
		}
		if p.Typing {
			tags = append(tags, yellow+"✎ typing"+reset)
		} else if p.Composing {
			tags = append(tags, dim+"… chatting"+reset)
		}
		// Where they are: the tabs they hold the helm of (the host holds
		// every tab nobody else does, so for the host: the tab shown).
		var where []string
		for _, t := range s.Tabs {
			if !p.Host && t.Helm == p.ID {
				where = append(where, cyan+"⎈ "+tabName(t.Window)+reset)
			}
		}
		if p.Host && s.Current != "" {
			where = append(where, dim+"on "+tabName(s.Current)+reset)
		}
		left := fmt.Sprintf("  %s %s%s%s", dot, bold, truncate(name, 14), reset)
		if len(tags) > 0 {
			left += " " + strings.Join(tags, " ")
		}
		right := strings.Join(where, " ")
		row := left
		if right != "" {
			pad := w - width(left) - width(right) - 1
			if pad < 1 {
				right = truncateStyled(right, max(0, w-width(left)-2))
				pad = 1
			}
			row += strings.Repeat(" ", pad) + right
		}
		out = append(out, truncateStyled(row, w))
	}
	if s.Host {
		for _, j := range s.Joins {
			msg := fmt.Sprintf("  %s?%s %s wants to join — Ctrl+Shift+M", yellow+bold, reset, shortName(j.Name))
			out = append(out, truncateStyled(msg, w))
		}
	}
	for _, r := range s.Requests {
		msg := fmt.Sprintf("  %s✋%s %s asks for the helm of %s", yellow, reset, shortName(r.Name), tabName(r.Window))
		out = append(out, truncateStyled(msg, w))
	}
	if s.Last != nil {
		mark := dim + "✉" + reset
		if s.Unread > 0 {
			mark = yellow + bold + "✉ " + strconv.Itoa(s.Unread) + reset
		}
		from := shortName(s.Last.Name)
		if from == "" {
			from = "host"
		}
		msg := fmt.Sprintf("  %s %s%s:%s %s", mark, bold, from, reset, strings.Join(strings.Fields(s.Last.Text), " "))
		out = append(out, truncateStyled(msg, w))
	}
	return out
}

// shortName drops the @machine from user@machine.
func shortName(n string) string {
	if i := strings.IndexByte(n, '@'); i > 0 {
		return n[:i]
	}
	return n
}

// truncateStyled cuts a string with color codes to w cells.
func truncateStyled(s string, w int) string {
	if width(s) <= w {
		return s
	}
	if w <= 0 {
		return ""
	}
	var b strings.Builder
	n := 0
	for i := 0; i < len(s); {
		if s[i] == 0x1b { // copy escape sequences whole
			j := i + 1
			if j < len(s) && s[j] == '[' {
				j++
				for j < len(s) && (s[j] < 0x40 || s[j] > 0x7e) {
					j++
				}
			}
			b.WriteString(s[i:min(j+1, len(s))])
			i = j + 1
			continue
		}
		r, sz := utf8.DecodeRuneInString(s[i:])
		if n == w-1 {
			b.WriteString("…")
			break
		}
		b.WriteRune(r)
		n++
		i += sz
	}
	return b.String() + reset
}

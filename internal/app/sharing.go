package app

import (
	"os"
	"strings"
	"time"

	"github.com/go-gl/glfw/v3.3/glfw"

	"github.com/zach-monroe/germ/internal/control"
	"github.com/zach-monroe/germ/internal/share"
)

// Session sharing, germ side. Hosting runs as `germ share` in a pane (it
// only needs tmux); it reports who's connected through the control socket
// (set_status). Joining runs germ's tmux controller over the encrypted
// tunnel instead of a local tmux process.

type statusText struct {
	text  string
	until time.Time // zero: until replaced
}

// setStatus shows text on the right of the tab bar, for ttl (0: until
// replaced). Safe from any goroutine.
func (a *App) setStatus(text string, ttl time.Duration) {
	st := &statusText{text: text}
	if ttl > 0 {
		st.until = time.Now().Add(ttl)
	}
	a.status.Store(st)
	a.forceRedrawAsync()
}

func (a *App) statusLine() string {
	st := a.status.Load()
	if st == nil || (!st.until.IsZero() && time.Now().After(st.until)) {
		return ""
	}
	return st.text
}

// forceRedrawAsync asks the main loop to redraw everything.
func (a *App) forceRedrawAsync() {
	a.redrawAll.Store(true)
	glfw.PostEmptyEvent()
}

// presenceText summarizes who's in a session.
func presenceText(ps []share.Participant, prefix string) string {
	var names []string
	for _, p := range ps {
		n := p.Name
		if i := strings.IndexByte(n, '@'); i > 0 {
			n = n[:i]
		}
		if p.ReadOnly {
			n += " (watching)"
		}
		names = append(names, n)
	}
	return prefix + strings.Join(names, ", ")
}

// startRemote attaches to a shared session.
func (a *App) startRemote(g *share.Guest) {
	c := a.newTmuxController(g, g, g.Welcome.Session, func() { g.Close() })
	c.remote = g
	label := "⇄ " + g.Welcome.Host
	if g.Welcome.ReadOnly {
		label += " (watching)"
	}
	a.setStatus(label, 0)
	g.OnPresence = func(ps []share.Participant) {
		a.setStatus(presenceText(ps, "⇄ "), 0)
	}
	if ps := g.People(); len(ps) > 0 { // arrived before the handler was set
		g.OnPresence(ps)
	}
	c.begin()
}

// guestName is how this user appears to others.
func guestName() string {
	user := os.Getenv("USER")
	host, _ := os.Hostname()
	if user == "" {
		user = "guest"
	}
	if host == "" {
		return user
	}
	return user + "@" + host
}

// shareSession opens a pane running `germ share` for this tmux session.
func (a *App) shareSession() {
	if a.tmux == nil || a.tmux.remote != nil {
		go desktopNotify("germ", "Sharing needs germ's tmux mode ([tmux] enabled = true).")
		return
	}
	self, err := os.Executable()
	if err != nil {
		return
	}
	noop := func(*control.Response) {}
	a.ctlOpen(control.Request{Where: "down", Size: 30, Command: []string{self, "share"}},
		a.workPane(), noop, noop)
}

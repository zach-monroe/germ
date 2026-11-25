package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
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
	text := ""
	if st != nil && (st.until.IsZero() || time.Now().Before(st.until)) {
		text = st.text
	}
	return a.chatStatus(text)
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
		if p.Typing {
			n += " ✎"
		}
		names = append(names, n)
	}
	return prefix + strings.Join(names, ", ")
}

// startRemote attaches to a shared session.
func (a *App) startRemote(g *share.Guest) {
	c := a.newTmuxController(g, g, g.Welcome.Session, func() { g.Close() })
	c.remote = g
	c.follow = true
	g.OnPresence = func(ps []share.Participant) {
		a.mainThread(func() {
			a.chatPeople(ps)
			a.updateGuestStatus()
		})
	}
	g.OnDisconnect = func() {
		c.offline.Store(true)
		a.setStatus("⇄ reconnecting…", 0)
	}
	a.startGuestChat(g)
	a.updateGuestStatus()
	c.begin()
	c.findHost()
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

// shareSession shares this tmux session: `germ share` runs in the
// background (in no pane, so guests can't see it) and the controls are in
// the chat panel. Already sharing, it shows the panel.
func (a *App) shareSession() {
	if a.tmux == nil || a.tmux.remote != nil {
		go desktopNotify("germ", "Sharing needs germ's tmux mode ([tmux] enabled = true).")
		return
	}
	c := a.chat
	if c.host && c.active() {
		if p := a.sharePane(); p != nil { // germ share in a pane, from before
			a.focusPane(p)
			return
		}
		a.openChat(true)
		return
	}
	if time.Since(a.shareStarted) < 30*time.Second {
		a.chatToast("◉ starting to share…", 4*time.Second)
		return
	}
	self, err := os.Executable()
	if err != nil {
		return
	}
	cmd := exec.Command(self, "share", "-session", a.tmux.session, "-headless")
	env := []string{"GERM_SOCKET=" + control.SocketPath(os.Getpid())}
	if a.opts.ConfigPath != "" {
		env = append(env, "GERM_CONFIG="+a.opts.ConfigPath)
	}
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if k != "TMUX" && k != "TMUX_PANE" && k != "GERM_PANE_ID" && k != "GERM_SOCKET" && k != "GERM_CONFIG" {
			env = append(env, kv)
		}
	}
	cmd.Env = env
	os.MkdirAll(StateDir(), 0o700)
	logPath := filepath.Join(StateDir(), "share.log")
	if f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600); err == nil {
		cmd.Stdout, cmd.Stderr = f, f
		defer f.Close()
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} // keeps sharing if this window closes
	if err := cmd.Start(); err != nil {
		a.chatToast("couldn't start sharing: "+err.Error(), 8*time.Second)
		return
	}
	a.shareStarted = time.Now()
	a.chatToast("◉ starting to share…", 10*time.Second)
	go func() {
		err := cmd.Wait()
		a.mainThread(func() {
			a.shareStarted = time.Time{}
			if err != nil {
				a.chatToast("sharing stopped: "+lastLine(logPath), 12*time.Second)
				go desktopNotify("germ", "Sharing stopped: "+lastLine(logPath))
			}
		})
	}()
}

// lastLine is the last non-empty line of the file at path.
func lastLine(path string) string {
	b, _ := os.ReadFile(path)
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	l := strings.TrimSpace(lines[len(lines)-1])
	l = strings.TrimPrefix(l, "germ share: ")
	if l == "" {
		return "see " + path
	}
	return l
}

// reconnected re-reads a remote session after its stream restarted.
func (c *tmuxController) reconnected() {
	c.begin()
	for id, p := range c.panes {
		c.capture(id, p)
	}
	c.findHost()
	c.a.setStatus(presenceText(c.remote.People(), "⇄ "), 0)
}

// findHost learns the host session's ID (to follow it) and, when
// following, jumps to the host's current window.
func (c *tmuxController) findHost() {
	host := c.remote.Welcome.Session
	c.command(`display -p -t `+tmuxQuote(host)+` "#{session_id}	#{window_id}"`, func(lines []string, failed bool) {
		if failed || len(lines) == 0 {
			return
		}
		sid, wid, _ := strings.Cut(lines[0], "\t")
		c.hostSessionID = sid
		if c.follow && wid != "" {
			c.command("select-window -t "+c.windowTarget(wid), nil)
			// If we're already there tmux says nothing, and after a
			// reconnect germ may be showing another tab.
			if id, ok := parseTmuxID(wid); ok {
				c.showWindow(id)
			}
		}
	})
}

// toggleFollow switches a guest between following the host's window and
// looking around on their own.
func (a *App) toggleFollow() bool {
	c := a.tmux
	if c == nil || c.remote == nil {
		return false
	}
	c.follow = !c.follow
	if c.follow {
		c.findHost()
	}
	a.updateGuestStatus()
	return true
}

// updateGuestStatus shows who's here and whether we follow the host.
func (a *App) updateGuestStatus() {
	c := a.tmux
	if c == nil || c.remote == nil {
		return
	}
	text := presenceText(c.remote.People(), "⇄ ")
	if text == "⇄ " {
		text = "⇄ " + c.remote.Welcome.Host
	}
	if !c.follow {
		text += " · free (Ctrl+Shift+G follows)"
	}
	a.setStatus(text, 0)
}

// mainThread runs fn on the main thread soon. Safe from any goroutine.
func (a *App) mainThread(fn func()) {
	a.tasksMu.Lock()
	a.tasks = append(a.tasks, fn)
	a.tasksMu.Unlock()
	glfw.PostEmptyEvent()
}

func (a *App) runTasks() {
	a.tasksMu.Lock()
	tasks := a.tasks
	a.tasks = nil
	a.tasksMu.Unlock()
	for _, fn := range tasks {
		fn()
	}
}

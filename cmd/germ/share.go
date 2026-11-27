package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/zach-monroe/germ/internal/config"
	"github.com/zach-monroe/germ/internal/control"
	"github.com/zach-monroe/germ/internal/desktop"
	"github.com/zach-monroe/germ/internal/share"
)

func loadConfig() config.Config {
	path := os.Getenv("GERM_CONFIG")
	if path == "" {
		path = config.Path()
	}
	cfg, _, _ := config.Load(path)
	return cfg
}

// logTo, when set, takes log lines (tunnels, reconnects) for the share pane.
var logTo func(format string, a ...any)

// runShare shares the current tmux session until interrupted.
func runShare(args []string) int {
	cfg := loadConfig()
	fs := flag.NewFlagSet("germ share", flag.ExitOnError)
	relay := fs.String("relay", cfg.Share.Relay, "relay server URL (default from [share] relay; none: serve from this machine)")
	mode := fs.String("mode", cfg.Share.Mode, "without a relay: auto (Tailscale Funnel if set up, else a Cloudflare tunnel), tailscale, tunnel, or direct")
	session := fs.String("session", "", "tmux session to share (default: the one this pane is in)")
	headless := fs.Bool("headless", false, "run in the background, with the controls in the host's germ window (germ starts it this way)")
	fs.Parse(args)
	var u *url.URL
	if *relay != "" {
		var err error
		u, err = url.Parse(*relay)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			fmt.Fprintf(os.Stderr, "germ share: bad relay URL %q\n", *relay)
			return pause(1)
		}
	}
	// In a germ window, sharing runs in the background and the controls are
	// in the window, where guests can't see them (a pane here would be in
	// the shared session, drafts and all).
	if !*headless && *session == "" && os.Getenv("GERM_SOCKET") != "" {
		if c, err := control.Connect(); err == nil {
			_, err := c.Call(control.Request{Cmd: control.CmdShareStart})
			c.Close()
			if err == nil {
				fmt.Println("Sharing in the background: the link, who's here and the chat are in your germ window (Ctrl+Shift+M).")
				return 0
			}
		}
	}
	var tmuxArgs []string
	inSession := *session == "" // sharing the session this pane is in
	if *session == "" {
		if os.Getenv("TMUX") == "" {
			fmt.Fprintln(os.Stderr, "germ share: run this inside germ's tmux mode (or pass -session NAME)")
			return pause(1)
		}
		out, err := exec.Command("tmux", "display", "-p", "#S").Output()
		if err != nil {
			fmt.Fprintln(os.Stderr, "germ share: can't find the tmux session:", err)
			return pause(1)
		}
		*session = strings.TrimSpace(string(out))
	} else if os.Getenv("TMUX") == "" && cfg.Tmux.Socket != "" {
		tmuxArgs = []string{"-L", cfg.Tmux.Socket}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	logf := func(format string, a ...any) { // the share pane's log (or the window's) once it's up
		if logTo != nil {
			logTo(format, a...)
			return
		}
		fmt.Printf("%s%s%s  %s\n", dim, time.Now().Format("15:04"), reset, fmt.Sprintf(format, a...))
	}

	h, err := share.NewHost(u, *session)
	if err != nil {
		fmt.Fprintln(os.Stderr, "germ share:", err)
		return pause(1)
	}
	via := ""
	if u != nil {
		via = "via relay " + u.Host
	} else {
		// No relay: serve the session from this machine.
		fmt.Printf("%sopening a connection…%s\n", dim, reset)
		exp, local, err := serveLocally(ctx, h, cfg.Share, *mode, logf)
		if err != nil {
			fmt.Fprintln(os.Stderr, "germ share:", err)
			return pause(1)
		}
		defer exp.Close()
		h.Relay, h.Dial = exp.URL, local
		switch exp.Mode {
		case "tailscale":
			via = "through Tailscale Funnel (" + exp.URL.Host + ")"
		case "tunnel":
			via = "through a Cloudflare quick tunnel"
		default:
			via = "directly from this machine (" + exp.URL.Host + ", plain http)"
		}
	}
	h.Name = share.Whoami()
	h.Approval = cfg.Share.Approve
	h.TmuxArgs = tmuxArgs
	if inSession && !*headless { // guests don't type into this pane
		h.Protect = os.Getenv("TMUX_PANE")
	}
	// The window learns about the session (the panel, the tab bar, the
	// helm, alerts) and sends what the host does there.
	br := newShareBridge(h, *session, via, !*headless, stop)
	var pane *sharePane
	if *headless {
		logTo = func(format string, a ...any) {
			line := fmt.Sprintf(format, a...)
			fmt.Printf("%s  %s\n", time.Now().Format("15:04:05"), line)
			br.notice(line)
		}
	} else {
		pane = newSharePane(h, br, *session, via, stop)
		logTo = pane.log
	}
	h.Log = logTo
	changed := func() {
		if pane != nil {
			pane.changed()
		}
		br.changed()
	}
	h.OnRequest = func(r share.JoinRequest) {
		if pane != nil {
			pane.asked(fmt.Sprintf("join/%d", r.ID), r.Name+" wants to join")
		}
		br.changed()
	}
	h.OnHelmRequest = func(r share.HelmRequest) {
		if pane != nil {
			pane.asked(fmt.Sprintf("helm/%d", r.ID), share.ShortName(r.Name)+" asks for the helm")
		}
		br.changed()
	}
	h.OnChat = func(m share.ChatMsg) {
		if pane != nil {
			pane.chat(m)
		}
		br.changed()
	}
	h.OnState = func(share.State) { changed() }
	h.OnChange = func(ps []share.Participant) {
		br.status(presence(ps))
		changed()
	}
	ensureURLHandler(true)
	if pane != nil {
		copyOSC52(h.Link().String())
		who := "you let each person in"
		if h.Approval == "none" {
			who = "anyone with it can join"
		}
		pane.log("The invite link is on your clipboard: %s, everyone can watch and chat, and you hand out the helm to type. Chat here; /help lists commands.", who)
		pane.log("Guests can see this pane: it's in the session you're sharing. In germ, Ctrl+Shift+P shares with the controls in the window instead.")
	}

	go func() { // keep the status alive (it expires if we die)
		t := time.NewTicker(60 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				br.status(presence(h.Participants()))
			}
		}
	}()
	go br.run(ctx)
	go func() { // the session went away (killed, the tmux server quit): so does sharing
		gone := 0
		for ctx.Err() == nil {
			time.Sleep(5 * time.Second)
			if h.SessionAlive() {
				gone = 0
			} else if gone++; gone >= 2 {
				h.Log("the tmux session %q ended", *session)
				stop()
				return
			}
		}
	}()
	// Serve guests, reconnecting to the relay if the connection drops;
	// guests reconnect on their own meanwhile.
	done := make(chan error, 1)
	go func() {
		wait := time.Second
		for {
			start := time.Now()
			err := h.Run(ctx)
			if ctx.Err() != nil {
				done <- nil
				return
			}
			if time.Since(start) > time.Minute {
				wait = time.Second
			}
			h.Log("%s; reconnecting in %s", err, wait)
			br.status("◉ contagious (reconnecting…)")
			select {
			case <-ctx.Done():
				done <- nil
				return
			case <-time.After(wait):
			}
			wait = min(wait*2, 30*time.Second)
		}
	}()
	if pane != nil {
		pane.run(ctx)
	} else {
		<-ctx.Done()
	}
	logTo = nil
	err = <-done
	br.end()
	return finishShare(err)
}

// tabName is "tab 2 (zsh)".
func tabName(st share.State, window string) string {
	if t := st.Tab(window); t != nil {
		if t.Name != "" {
			return fmt.Sprintf("tab %d (%s)", t.Index, t.Name)
		}
		return fmt.Sprintf("tab %d", t.Index)
	}
	return "a tab"
}

// saveChat saves the chat to a file, tells the window, and returns what
// happened.
func saveChat(h *share.Host, br *shareBridge) string {
	path, err := h.SaveChat()
	if err != nil {
		msg := "couldn't save the chat: " + err.Error()
		br.notice(msg)
		return msg
	}
	h.Event("%s saved the chat", share.ShortName(h.Name))
	msg := "chat saved to " + tildePath(path)
	br.notice(msg)
	return msg
}

// shareBridge keeps the host's germ window up to date with the session
// (share_update), and does what the host does there (share_wait). It
// finds the window again if it closes and another opens.
type shareBridge struct {
	h       *share.Host
	session string
	via     string
	server  string // the tmux server's socket
	pane    bool   // germ share runs in a pane (Update.Pane)
	stop    func() // /stop in the window
	kick    chan struct{}
	mu      sync.Mutex
	path    string          // the window's control socket ("" without one)
	conn    *control.Client // for pushes (run's goroutine only)
	seq     uint64          // the window's last chat message
	ok      bool            // the window shows the session
	notices []string
}

func newShareBridge(h *share.Host, session, via string, pane bool, stop func()) *shareBridge {
	return &shareBridge{h: h, session: session, via: via, server: h.Server(), pane: pane, stop: stop, kick: make(chan struct{}, 1)}
}

// window reports whether a germ window is showing the session's chat.
func (b *shareBridge) window() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.ok
}

func (b *shareBridge) changed() {
	select {
	case b.kick <- struct{}{}:
	default:
	}
}

func (b *shareBridge) notice(text string) {
	b.mu.Lock()
	b.notices = append(b.notices, text)
	b.mu.Unlock()
	b.changed()
}

// call sends one request to the window, on a connection of its own.
func (b *shareBridge) call(req control.Request) (*control.Response, error) {
	b.mu.Lock()
	path := b.path
	b.mu.Unlock()
	if path == "" {
		return nil, fmt.Errorf("no germ window")
	}
	c, err := control.Dial(path)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	return c.Call(req)
}

// status shows text on the right of the window's tab bar.
func (b *shareBridge) status(text string) {
	b.call(control.Request{Cmd: control.CmdSetStatus, Text: text, TTL: 90})
}

func (b *shareBridge) run(ctx context.Context) {
	go b.waitLoop(ctx)
	t := time.NewTicker(20 * time.Second) // the window forgets a share it doesn't hear from
	defer t.Stop()
	for {
		if b.conn == nil {
			b.find()
		} else if !b.push(false) {
			b.conn.Close()
			b.conn = nil
		}
		retry := 24 * time.Hour
		if b.conn == nil || !b.window() {
			retry = 3 * time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-b.kick:
		case <-t.C:
		case <-time.After(retry):
		}
		time.Sleep(30 * time.Millisecond) // coalesce bursts
	}
}

// find looks for the window showing the session: $GERM_SOCKET's first,
// then any other.
func (b *shareBridge) find() {
	var paths []string
	if p := os.Getenv("GERM_SOCKET"); p != "" {
		paths = append(paths, p)
	}
	for _, p := range append(paths, control.Sockets()...) {
		c, err := control.Dial(p)
		if err != nil {
			continue
		}
		b.mu.Lock()
		b.conn, b.path, b.seq = c, p, 0 // a new window gets the whole chat
		b.mu.Unlock()
		if b.push(false) {
			b.status(presence(b.h.Participants()))
			return
		}
		c.Close()
		b.mu.Lock()
		b.conn, b.path = nil, ""
		b.mu.Unlock()
	}
}

// push sends the window what changed. It reports false if the window is
// gone or won't show the session (then the next window gets it).
func (b *shareBridge) push(ended bool) bool {
	b.mu.Lock()
	since := b.seq
	notices := b.notices
	b.notices = nil
	b.mu.Unlock()
	u := &share.Update{Session: b.session, Link: b.h.Link().String(), People: b.h.Participants(),
		State: b.h.State(), Joins: b.h.JoinRequests(), Ended: ended, Notice: strings.Join(notices, "; "),
		Via: b.via, Server: b.server}
	if b.pane {
		u.Pane = os.Getenv("TMUX_PANE")
	}
	for _, m := range b.h.Chat() {
		if m.Seq > since {
			u.Chat = append(u.Chat, m)
		}
	}
	resp, err := b.conn.Call(control.Request{Cmd: control.CmdShareUpdate, Share: u})
	b.mu.Lock()
	defer b.mu.Unlock()
	b.ok = err == nil
	if err != nil {
		b.notices = append(notices, b.notices...)
		return false
	}
	b.seq = resp.Seq
	return true
}

// waitLoop does what the host does in the window: chat, the helm, joins,
// the link, removing people, stopping.
func (b *shareBridge) waitLoop(ctx context.Context) {
	var c *control.Client
	for ctx.Err() == nil {
		if c == nil {
			b.mu.Lock()
			path := b.path
			b.mu.Unlock()
			if path != "" {
				c, _ = control.Dial(path)
			}
			if c == nil {
				time.Sleep(2 * time.Second)
				continue
			}
		}
		resp, err := c.Call(control.Request{Cmd: control.CmdShareWait})
		if err != nil {
			c.Close()
			c = nil
			time.Sleep(2 * time.Second)
			continue
		}
		for _, a := range resp.Actions {
			switch a.Op {
			case share.OpSave:
				saveChat(b.h, b)
			case share.OpJoin:
				b.h.Decide(a.Request, a.Allow)
			case share.OpRotate:
				if err := b.h.RotateLinks(); err != nil {
					b.notice("couldn't make a new link: " + err.Error())
				} else {
					b.notice("new link, copied: the old one no longer works; people here stay")
				}
			case share.OpKick:
				for _, p := range b.h.Participants() {
					if p.ID == a.Person && !p.Host {
						b.h.Remove(p.ID)
						b.notice("removed " + share.ShortName(p.Name) + " · /newlink so they can't come back")
					}
				}
			case share.OpStop:
				b.stop()
			default:
				b.h.Act(a)
			}
		}
		if len(resp.Actions) > 0 {
			b.changed()
		}
	}
	if c != nil {
		c.Close()
	}
}

// end tells the window sharing stopped.
func (b *shareBridge) end() {
	if b.conn != nil {
		b.push(true)
	}
	b.status("")
}

func finishShare(err error) int {
	if err != nil {
		fmt.Fprintln(os.Stderr, "germ share:", err)
		return pause(1)
	}
	fmt.Println("stopped sharing · all clear")
	return 0
}

func presence(ps []share.Participant) string {
	if len(ps) <= 1 {
		return "◉ contagious"
	}
	var names []string
	for _, p := range ps[1:] {
		n := share.ShortName(p.Name)
		if p.Typing {
			n += " ✎"
		}
		names = append(names, n)
	}
	return "◉ contagious · " + strings.Join(names, ", ")
}

// copyOSC52 puts text on the clipboard through the terminal.
func copyOSC52(text string) {
	fmt.Printf("\x1b]52;c;%s\x07", base64.StdEncoding.EncodeToString([]byte(text)))
}

// pause keeps a failing pane open long enough to read the error when germ
// launched it.
func pause(code int) int {
	if os.Getenv("GERM_SOCKET") != "" && isTTY(0) {
		fmt.Fprint(os.Stderr, "\nPress Enter to close.")
		var b [1]byte
		os.Stdin.Read(b[:])
	}
	return code
}

// prepareJoin parses an invite and connects; germ then opens a window on
// the shared session.
func prepareJoin(link string) (*share.Guest, error) {
	l, err := share.ParseLink(link)
	if err != nil {
		return nil, err
	}
	ensureURLHandler(true)
	ctx := context.Background()
	return share.Join(ctx, l, share.Whoami(), func() {
		msg := "waiting for the host to let you in…"
		fmt.Fprintln(os.Stderr, "germ:", msg)
		desktop.Notify("germ", msg) // launched from a browser: no terminal
	})
}

// ensureURLHandler registers germ for germ:// links (so "Open in germ"
// on an invite page works). quiet is the automatic call made when hosting
// or joining: it prints nothing, and skips temporary builds (go run, a
// test's binary) so they don't take over links from the installed germ.
func ensureURLHandler(quiet bool) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	if quiet && temporaryBuild(self) {
		return nil
	}
	dataHome := os.Getenv("XDG_DATA_HOME")
	if dataHome == "" {
		home, _ := os.UserHomeDir()
		dataHome = filepath.Join(home, ".local", "share")
	}
	dir := filepath.Join(dataHome, "applications")
	path := filepath.Join(dir, "germ-join.desktop")
	entry := fmt.Sprintf(`[Desktop Entry]
Type=Application
Name=germ
Comment=Join a shared germ terminal session
Exec=%s join %%u
MimeType=x-scheme-handler/germ;
NoDisplay=true
Terminal=false
`, self)
	if old, err := os.ReadFile(path); err == nil && string(old) == entry {
		if !quiet {
			fmt.Println("germ:// links already open germ")
		}
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(entry), 0o644); err != nil {
		return err
	}
	if p, err := exec.LookPath("xdg-mime"); err == nil {
		exec.Command(p, "default", "germ-join.desktop", "x-scheme-handler/germ").Run()
	}
	if p, err := exec.LookPath("update-desktop-database"); err == nil {
		exec.Command(p, dir).Run()
	}
	if !quiet {
		fmt.Println("germ:// links now open germ (", path, ")")
	}
	return nil
}

// serveLocally runs a private relay for this share on this machine and
// makes it reachable: directly (UPnP port forward) or through a tunnel.
// It returns the exposure and the local URL the host connects to.
func serveLocally(ctx context.Context, h *share.Host, sc config.Share, mode string, logf func(string, ...any)) (*share.Exposure, *url.URL, error) {
	if mode == "" {
		mode = "auto"
	}
	addr := fmt.Sprintf(":%d", sc.Port)
	if mode == "tunnel" {
		addr = fmt.Sprintf("127.0.0.1:%d", sc.Port) // only the tunnel needs to reach it
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, nil, fmt.Errorf("listening on %s: %w", addr, err)
	}
	rl := share.NewRelay()
	rl.OnlyRoom = h.Room
	rl.HostToken = randomToken()
	h.HostToken = rl.HostToken
	rl.Log.SetOutput(io.Discard)
	srv := &http.Server{Handler: rl.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go srv.Serve(ln)
	go func() { <-ctx.Done(); srv.Close() }()
	port := ln.Addr().(*net.TCPAddr).Port
	exp, err := share.Expose(ctx, port, share.ExposeOptions{Mode: mode, PublicAddress: sc.PublicAddress, Log: logf})
	if err != nil {
		srv.Close()
		return nil, nil, err
	}
	return exp, &url.URL{Scheme: "http", Host: fmt.Sprintf("127.0.0.1:%d", port)}, nil
}

func randomToken() string {
	b := make([]byte, 24)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// temporaryBuild reports whether the binary at self lives in a temporary
// directory, as go run and test builds do.
func temporaryBuild(self string) bool {
	if p, err := filepath.EvalSymlinks(self); err == nil {
		self = p
	}
	tmp := os.TempDir()
	if p, err := filepath.EvalSymlinks(tmp); err == nil {
		tmp = p
	}
	rel, err := filepath.Rel(tmp, self)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

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

func whoami() string {
	user := os.Getenv("USER")
	if user == "" {
		user = "someone"
	}
	if host, _ := os.Hostname(); host != "" {
		return user + "@" + host
	}
	return user
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
	logf := func(format string, a ...any) { // the share pane's log once it's up
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
	h.Name = whoami()
	h.Approval = cfg.Share.Approve
	if h.Approval == "typing" { // from before the helm: everyone can type now (with it)
		h.Approval = "all"
	}
	h.TmuxArgs = tmuxArgs
	if inSession { // guests don't type into this pane
		h.Protect = os.Getenv("TMUX_PANE")
	}
	ctl, _ := control.Connect()
	var ctlMu sync.Mutex // one call at a time on the connection
	status := func(text string) {
		if ctl != nil {
			ctlMu.Lock()
			ctl.Call(control.Request{Cmd: control.CmdSetStatus, Text: text, TTL: 90})
			ctlMu.Unlock()
		}
	}
	// The window learns about the session (the tab bar, the helm, alerts)
	// and sends what the host does there (Ctrl+Shift+O, the palette).
	br := newShareBridge(h, *session, ctl)
	pane := newSharePane(h, br, ctl, *session, via, stop)
	logTo = pane.log
	h.Log = pane.log
	h.OnRequest = func(r share.JoinRequest) {
		pane.asked(fmt.Sprintf("join/%d", r.ID), r.Name+" wants to join")
		br.changed()
	}
	h.OnHelmRequest = func(r share.HelmRequest) {
		pane.asked(fmt.Sprintf("helm/%d", r.ID), share.ShortName(r.Name)+" asks for the helm")
		br.changed()
	}
	h.OnChat = func(m share.ChatMsg) {
		pane.chat(m)
		br.changed()
	}
	h.OnState = func(share.State) { pane.changed(); br.changed() }
	h.OnChange = func(ps []share.Participant) {
		status(presence(ps))
		pane.changed()
		br.changed()
	}
	ensureURLHandler(true)
	copyOSC52(h.Link().String())
	who := "you let each person in"
	if h.Approval == "none" {
		who = "anyone with it can join"
	}
	pane.log("The invite link is on your clipboard: %s, everyone can watch and chat, and you hand out the helm to type. Chat here; /help lists commands.", who)

	status("◉ contagious")
	go func() { // keep the status alive (it expires if we die)
		t := time.NewTicker(60 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				status(presence(h.Participants()))
			}
		}
	}()
	go br.run(ctx)
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
			status("◉ contagious (reconnecting…)")
			select {
			case <-ctx.Done():
				done <- nil
				return
			case <-time.After(wait):
			}
			wait = min(wait*2, 30*time.Second)
		}
	}()
	pane.run(ctx)
	logTo = nil
	err = <-done
	br.end()
	return finishShare(ctl, err)
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
// (share_update), and does what the host does there (share_wait).
type shareBridge struct {
	h       *share.Host
	session string
	ctl     *control.Client
	kick    chan struct{}
	mu      sync.Mutex
	seq     uint64 // the window's last chat message
	ok      bool   // the window understands share_update
	notices []string
}

func newShareBridge(h *share.Host, session string, ctl *control.Client) *shareBridge {
	return &shareBridge{h: h, session: session, ctl: ctl, kick: make(chan struct{}, 1)}
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

func (b *shareBridge) run(ctx context.Context) {
	if b.ctl == nil {
		return
	}
	c, err := control.Dial(b.ctl.Path) // a connection of its own: calls can't overlap
	if err != nil {
		return
	}
	defer c.Close()
	b.ctl = c
	go b.waitLoop(ctx)
	b.push(false)
	t := time.NewTicker(20 * time.Second) // the window forgets a share it doesn't hear from
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-b.kick:
		case <-t.C:
		}
		b.push(false)
		time.Sleep(30 * time.Millisecond) // coalesce bursts
	}
}

func (b *shareBridge) push(ended bool) {
	b.mu.Lock()
	since := b.seq
	notices := b.notices
	b.notices = nil
	b.mu.Unlock()
	u := &share.Update{Session: b.session, Link: b.h.Link().String(), People: b.h.Participants(),
		State: b.h.State(), Joins: b.h.JoinRequests(), Ended: ended, Notice: strings.Join(notices, "; "),
		Pane: os.Getenv("TMUX_PANE")}
	for _, m := range b.h.Chat() {
		if m.Seq > since {
			u.Chat = append(u.Chat, m)
		}
	}
	resp, err := b.ctl.Call(control.Request{Cmd: control.CmdShareUpdate, Share: u})
	b.mu.Lock()
	b.ok = err == nil
	if err == nil {
		b.seq = resp.Seq
	}
	b.mu.Unlock()
}

// waitLoop does what the host does in the window: chat, the helm, joins.
func (b *shareBridge) waitLoop(ctx context.Context) {
	var c *control.Client
	for ctx.Err() == nil {
		if c == nil {
			var err error
			if c, err = control.Dial(b.ctl.Path); err != nil {
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
	if b.ctl != nil {
		b.push(true)
	}
}

func finishShare(ctl *control.Client, err error) int {
	if ctl != nil {
		ctl.Call(control.Request{Cmd: control.CmdSetStatus, Text: ""})
		ctl.Close()
	}
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
	return share.Join(ctx, l, whoami(), func() {
		msg := "waiting for the host to let you in…"
		fmt.Fprintln(os.Stderr, "germ:", msg)
		if p, err := exec.LookPath("notify-send"); err == nil { // launched from a browser: no terminal
			exec.Command(p, "--app-name=germ", "germ", msg).Run()
		}
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

// notify shows a desktop notification, if notify-send is installed.
func notify(msg string) {
	if p, err := exec.LookPath("notify-send"); err == nil {
		exec.Command(p, "--app-name=germ", "germ", msg).Run()
	}
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

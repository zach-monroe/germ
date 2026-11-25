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
	logf := func(format string, a ...any) {
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
	h.Log = logf
	ctl, _ := control.Connect()
	var ctlMu sync.Mutex // one call at a time on the connection
	status := func(text string) {
		if ctl != nil {
			ctlMu.Lock()
			ctl.Call(control.Request{Cmd: control.CmdSetStatus, Text: text, TTL: 90})
			ctlMu.Unlock()
		}
	}
	// The window shows the chat and answers requests, if it's a germ that
	// can (else this pane does it all).
	br := newShareBridge(h, *session, ctl)
	asks := make(chan shareAsk, 16)
	h.OnRequest = func(r share.JoinRequest) {
		asks <- shareAsk{join: &r}
		br.changed()
	}
	h.OnHelmRequest = func(r share.HelmRequest) {
		asks <- shareAsk{helm: &r}
		br.changed()
	}
	h.OnChat = func(m share.ChatMsg) {
		printChat(m)
		br.changed()
	}
	h.OnState = func(share.State) { br.changed() }
	lastPeople := ""
	h.OnChange = func(ps []share.Participant) {
		status(presence(ps))
		var ids []string
		for _, p := range ps {
			ids = append(ids, fmt.Sprint(p.ID))
		}
		if k := strings.Join(ids, ","); k != lastPeople { // joins and leaves, not typing
			lastPeople = k
			printPeople(ps)
		}
		br.changed()
	}
	ensureURLHandler(true)

	printLinks := func() {
		l := h.Link()
		fmt.Printf("%sInvite link%s (on your clipboard):\n  %s%s%s\n", bold+green, reset, cyan, l, reset)
		fmt.Printf("%sAnyone with it can watch and chat. Typing takes the helm, which you hand out.%s\n\n", dim, reset)
		copyOSC52(l.String())
	}
	fmt.Printf("%s◆ you're contagious%s · tmux session %q %s\n", bold+blue, reset, *session, via)
	fmt.Printf("%sAnyone with the link catches it in their browser (or in germ). Nothing to install.%s\n\n", dim, reset)
	printLinks()
	help := func() {
		if br.window() {
			fmt.Printf("%sChat and requests are in your germ window too: Ctrl+Shift+M chats, Ctrl+Shift+O takes the helm back.%s\n", dim, reset)
		}
		fmt.Printf("  %sc%s copy link  %s1-9%s remove someone  %sr%s new link  %ss%s save the chat  %sq%s stop sharing\n\n",
			bold, reset, bold, reset, bold, reset, bold, reset, bold, reset)
	}
	help()

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
	keys := make(chan byte)
	go func() {
		for {
			keys <- readKey()
		}
	}()
	var asking []shareAsk // waiting for y/n, oldest first
	ask := func() {
		for len(asking) > 0 && !asking[0].pending(h) {
			asking = asking[1:] // answered in the window, or gone
		}
		if len(asking) == 0 {
			return
		}
		q := asking[0].question(h)
		fmt.Printf("\a\n%s» %s [y/N]%s ", bold+yel, q, reset)
		status("◉ contagious · " + q)
		if !br.window() {
			go notify(q)
		}
	}
	prune := time.NewTicker(time.Second)
	defer prune.Stop()
	finish := func(err error) int {
		br.end()
		return finishShare(ctl, err)
	}
	for {
		select {
		case err := <-done:
			return finish(err)
		case r := <-asks:
			if br.window() {
				continue // asked in the window (the chat panel)
			}
			asking = append(asking, r)
			if len(asking) == 1 {
				ask()
			}
		case <-prune.C:
			if len(asking) > 0 && !asking[0].pending(h) {
				fmt.Printf("%s(answered)%s\n", dim, reset)
				asking = asking[1:]
				status(presence(h.Participants()))
				ask()
			}
		case k := <-keys:
			if len(asking) > 0 && (k == 'y' || k == 'n' || k == 'Y' || k == 'N' || k == '\r') {
				r := asking[0]
				asking = asking[1:]
				allow := k == 'y' || k == 'Y'
				if allow {
					fmt.Println("yes")
				} else {
					fmt.Println("no")
				}
				if !r.answer(h, allow) {
					fmt.Printf("%s(already answered, or they'd gone)%s\n", dim, reset)
				}
				status(presence(h.Participants()))
				ask()
				continue
			}
			switch {
			case k == 'q' || k == 3 || k == 4:
				stop()
				return finish(<-done)
			case k == 'c':
				copyOSC52(h.Link().String())
				fmt.Println("copied the invite link")
			case k == 's':
				saveChat(h, br)
			case k == 'r':
				if err := h.RotateLinks(); err == nil {
					fmt.Printf("%snew link (the old one no longer works; people already here stay):%s\n\n", bold, reset)
					printLinks()
					br.changed()
				}
			case k >= '1' && k <= '9':
				guests := h.Participants()[1:]
				n := int(k - '1')
				if n >= len(guests) {
					fmt.Printf("%sno person %c (press a number from the list)%s\n", dim, k, reset)
					continue
				}
				g := guests[n]
				fmt.Printf("Remove %s%s%s? [y/N] ", bold, g.Name, reset)
				if <-keys != 'y' {
					fmt.Println("no")
					continue
				}
				fmt.Println("yes")
				h.Remove(g.ID)
				fmt.Printf("Also make a new link so they can't rejoin? [y/N] ")
				if <-keys == 'y' {
					fmt.Println("yes")
					if h.RotateLinks() == nil {
						printLinks()
						br.changed()
					}
				} else {
					fmt.Println("no")
				}
			case k == '?' || k == 'h':
				help()
			}
		}
	}
}

// shareAsk is a question for the host: someone wants to join, or wants
// the helm.
type shareAsk struct {
	join *share.JoinRequest
	helm *share.HelmRequest
}

func (q shareAsk) pending(h *share.Host) bool {
	if q.join != nil {
		for _, r := range h.JoinRequests() {
			if r.ID == q.join.ID {
				return true
			}
		}
		return false
	}
	for _, r := range h.State().Requests {
		if r.ID == q.helm.ID {
			return true
		}
	}
	return false
}

func (q shareAsk) question(h *share.Host) string {
	if q.join != nil {
		where := ""
		if q.join.Web {
			where = " (browser)"
		}
		return fmt.Sprintf("%s%s wants to join. Let them in?", q.join.Name, where)
	}
	return fmt.Sprintf("%s wants the helm of %s. Hand it over?", q.helm.Name, tabName(h.State(), q.helm.Window))
}

func (q shareAsk) answer(h *share.Host, allow bool) bool {
	if !q.pending(h) {
		return false
	}
	if q.join != nil {
		return h.Decide(q.join.ID, allow)
	}
	h.Act(share.Action{Op: share.OpDecide, Request: q.helm.ID, Allow: allow})
	return true
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

// printChat logs a chat message in the share pane.
func printChat(m share.ChatMsg) {
	at := time.UnixMilli(m.At).Format("15:04")
	if m.Event {
		fmt.Printf("%s%s  · %s%s\n", dim, at, m.Text, reset)
		return
	}
	name := share.ShortName(m.Name)
	lines := strings.Split(m.Text, "\n")
	fmt.Printf("%s%s%s  %s%s:%s %s\n", dim, at, reset, bold+cyan, name, reset, lines[0])
	for _, l := range lines[1:] {
		fmt.Printf("       %s\n", l)
	}
}

func saveChat(h *share.Host, br *shareBridge) {
	path, err := h.SaveChat()
	if err != nil {
		fmt.Println("couldn't save the chat:", err)
		br.notice("couldn't save the chat: " + err.Error())
		return
	}
	h.Event("%s saved the chat", share.ShortName(h.Name))
	fmt.Printf("chat saved to %s\n", tildePath(path))
	br.notice("chat saved to " + tildePath(path))
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
		State: b.h.State(), Joins: b.h.JoinRequests(), Ended: ended, Notice: strings.Join(notices, "; ")}
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

// printPeople lists guests with the numbers used to remove them.
func printPeople(ps []share.Participant) {
	if len(ps) <= 1 {
		fmt.Printf("%s  nobody's caught it yet%s\n", dim, reset)
		return
	}
	var b strings.Builder
	for i, p := range ps[1:] {
		typing := ""
		if p.Typing {
			typing = " ✎"
		}
		fmt.Fprintf(&b, "  %s%d%s %s%s", bold, i+1, reset, p.Name, typing)
	}
	fmt.Println(b.String())
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

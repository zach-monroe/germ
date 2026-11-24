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
	requests := make(chan share.JoinRequest, 8)
	h.OnRequest = func(r share.JoinRequest) { requests <- r }
	h.TmuxArgs = tmuxArgs
	ctl, _ := control.Connect()
	status := func(text string) {
		if ctl != nil {
			ctl.Call(control.Request{Cmd: control.CmdSetStatus, Text: text, TTL: 90})
		}
	}
	h.Log = logf
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
	}
	ensureURLHandler(true)

	printLinks := func() {
		rw, ro := h.Links()
		fmt.Printf("%sCan type%s (like handing over your keyboard - only for people you trust):\n  %s%s%s\n\n", bold+yel, reset, cyan, rw, reset)
		fmt.Printf("%sWatch only%s:\n  %s%s%s\n\n", bold+green, reset, cyan, ro, reset)
		copyOSC52(ro.String())
	}
	fmt.Printf("%s◆ sharing tmux session %q%s %s\n", bold+blue, *session, reset, via)
	fmt.Printf("%sGuests open a link in any browser (or in germ). Nothing to install.%s\n\n", dim, reset)
	printLinks()
	help := func() {
		fmt.Printf("%sThe watch-only link is on your clipboard.%s\n", dim, reset)
		fmt.Printf("  %sc%s copy can-type link  %sw%s copy watch link  %s1-9%s remove someone  %sr%s new links  %sq%s stop sharing\n\n",
			bold, reset, bold, reset, bold, reset, bold, reset, bold, reset)
	}
	help()

	status("◉ sharing")
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
			status("◉ sharing (reconnecting…)")
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
	var asking []share.JoinRequest // waiting for y/n, oldest first
	ask := func() {
		if len(asking) == 0 {
			return
		}
		r := asking[0]
		mode := "can type"
		if r.ReadOnly {
			mode = "watch only"
		}
		fmt.Printf("\a\n%s» %s wants to join (%s). Let them in? [y/N]%s ", bold+yel, r.Name, mode, reset)
		status("◉ sharing · " + r.Name + " wants to join")
		go notify(r.Name + " wants to join your germ session")
	}
	for {
		select {
		case err := <-done:
			return finishShare(ctl, err)
		case r := <-requests:
			asking = append(asking, r)
			if len(asking) == 1 {
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
				if !h.Decide(r.ID, allow) {
					fmt.Printf("%s(they'd already gone)%s\n", dim, reset)
				}
				status(presence(h.Participants()))
				ask()
				continue
			}
			switch {
			case k == 'q' || k == 3 || k == 4:
				stop()
				return finishShare(ctl, <-done)
			case k == 'c':
				rw, _ := h.Links()
				copyOSC52(rw.String())
				fmt.Println("copied the can-type link")
			case k == 'w':
				_, ro := h.Links()
				copyOSC52(ro.String())
				fmt.Println("copied the watch-only link")
			case k == 'r':
				if err := h.RotateLinks(); err == nil {
					fmt.Printf("%snew links (the old ones no longer work; people already here stay):%s\n\n", bold, reset)
					printLinks()
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
				fmt.Printf("Also make new links so they can't rejoin? [y/N] ")
				if <-keys == 'y' {
					fmt.Println("yes")
					if h.RotateLinks() == nil {
						printLinks()
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

func finishShare(ctl *control.Client, err error) int {
	if ctl != nil {
		ctl.Call(control.Request{Cmd: control.CmdSetStatus, Text: ""})
		ctl.Close()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "germ share:", err)
		return pause(1)
	}
	fmt.Println("stopped sharing")
	return 0
}

// printPeople lists guests with the numbers used to remove them.
func printPeople(ps []share.Participant) {
	if len(ps) <= 1 {
		fmt.Printf("%s  nobody else here%s\n", dim, reset)
		return
	}
	var b strings.Builder
	for i, p := range ps[1:] {
		mode := "can type"
		if p.ReadOnly {
			mode = "watching"
		}
		typing := ""
		if p.Typing {
			typing = " ✎"
		}
		fmt.Fprintf(&b, "  %s%d%s %s %s(%s)%s%s", bold, i+1, reset, p.Name, dim, mode, reset, typing)
	}
	fmt.Println(b.String())
}

func presence(ps []share.Participant) string {
	if len(ps) <= 1 {
		return "◉ sharing"
	}
	var names []string
	for _, p := range ps[1:] {
		n, _, _ := strings.Cut(p.Name, "@")
		if p.ReadOnly {
			n += " (watching)"
		}
		if p.Typing {
			n += " ✎"
		}
		names = append(names, n)
	}
	return "◉ sharing · " + strings.Join(names, ", ")
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
// on an invite page works). quiet skips output when already set up.
func ensureURLHandler(quiet bool) error {
	self, err := os.Executable()
	if err != nil {
		return err
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

package main

import (
	"context"
	"encoding/base64"
	"flag"
	"fmt"
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
	relay := fs.String("relay", cfg.Share.Relay, "relay URL (default from [share] relay)")
	session := fs.String("session", "", "tmux session to share (default: the one this pane is in)")
	fs.Parse(args)
	if *relay == "" {
		fmt.Fprintln(os.Stderr, `germ share: no relay configured. Deploy one (see deploy/relay in the germ repo, e.g. on Fly.io),
then add to ~/.config/germ/config.toml:

  [share]
  relay = "https://your-relay.fly.dev"`)
		return pause(1)
	}
	u, err := url.Parse(*relay)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		fmt.Fprintf(os.Stderr, "germ share: bad relay URL %q\n", *relay)
		return pause(1)
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

	h, err := share.NewHost(u, *session)
	if err != nil {
		fmt.Fprintln(os.Stderr, "germ share:", err)
		return pause(1)
	}
	h.Name = whoami()
	h.TmuxArgs = tmuxArgs
	ctl, _ := control.Connect()
	status := func(text string) {
		if ctl != nil {
			ctl.Call(control.Request{Cmd: control.CmdSetStatus, Text: text, TTL: 90})
		}
	}
	h.Log = func(format string, a ...any) {
		fmt.Printf("%s%s%s  %s\n", dim, time.Now().Format("15:04"), reset, fmt.Sprintf(format, a...))
	}
	h.OnChange = func(ps []share.Participant) {
		status(presence(ps))
	}
	ensureURLHandler(true)

	rw, ro := h.Links()
	fmt.Printf("%s◆ sharing tmux session %q%s via %s\n\n", bold+blue, *session, reset, u.Host)
	fmt.Printf("%sCan type%s (like handing over your keyboard - only for people you trust):\n  %s%s%s\n\n", bold+yel, reset, cyan, rw, reset)
	fmt.Printf("%sWatch only%s:\n  %s%s%s\n\n", bold+green, reset, cyan, ro, reset)
	fmt.Printf("%sThe watch-only link is on your clipboard. Ctrl+C stops sharing and disconnects everyone.%s\n\n", dim, reset)
	copyOSC52(ro.String())

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
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
	err = h.Run(ctx)
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
	return share.Join(ctx, l, whoami())
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

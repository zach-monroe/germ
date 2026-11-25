package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/zach-monroe/germ/internal/app"
	"github.com/zach-monroe/germ/internal/config"
	"github.com/zach-monroe/germ/internal/theme"
)

// `germ doctor` checks what germ depends on and says how to fix what's
// missing.

type check struct {
	level int // 0 ok, 1 warning, 2 problem
	what  string
	hint  string
}

func runDoctor(args []string) int {
	path := config.Path()
	if len(args) == 2 && args[0] == "-config" {
		path = args[1]
	}
	var checks []check
	add := func(level int, what, hint string) { checks = append(checks, check{level, what, hint}) }

	// Config
	cfg, warnings, err := config.Load(path)
	switch {
	case err != nil:
		add(2, "config "+path+": "+err.Error(), "fix the file; germ uses the defaults until then")
	case len(warnings) > 0:
		add(1, "config "+path+": "+strings.Join(warnings, "; "), "see config.example.toml for the keys")
	default:
		if _, statErr := os.Stat(path); statErr != nil {
			add(0, "config: none, using the defaults ("+path+")", "")
		} else {
			add(0, "config "+path, "")
		}
	}
	if _, err := cfg.ResolveTheme(); err != nil {
		add(2, err.Error(), "bundled themes: "+strings.Join(theme.Bundled(), ", "))
	}

	// Fonts
	if got, err := exec.Command("fc-match", "-f", "%{family}", cfg.Font.Family).Output(); err != nil {
		add(2, "fontconfig (fc-match) not found", "install fontconfig")
	} else if fam := strings.Split(string(got), ",")[0]; !strings.EqualFold(fam, cfg.Font.Family) {
		add(1, fmt.Sprintf("font %q isn't installed; using %q", cfg.Font.Family, fam),
			"install it, or set [font] family to one `fc-list : family` shows")
	} else {
		add(0, "font "+fam, "")
	}
	if got, _ := exec.Command("fc-match", "-f", "%{family}", "emoji:color=true").Output(); !strings.Contains(strings.ToLower(string(got)), "emoji") {
		add(1, "no color emoji font", "install Noto Color Emoji (noto-fonts-emoji / fonts-noto-color-emoji)")
	} else {
		add(0, "emoji font "+strings.Split(string(got), ",")[0], "")
	}

	// tmux
	if out, err := exec.Command("tmux", "-V").Output(); err != nil {
		add(2, "tmux not found: germ runs a plain shell (no tabs/splits that survive closing, no sharing)", "install tmux 3.2 or newer")
	} else {
		v := strings.TrimSpace(string(out))
		if tmuxAtLeast(v, 3, 2) {
			add(0, v, "")
		} else {
			add(1, v+" is old", "germ needs tmux 3.2 or newer for control mode")
		}
	}

	// Shell integration
	sh := filepath.Base(os.Getenv("SHELL"))
	if cfg.Shell.Program != "" {
		sh = filepath.Base(cfg.Shell.Program)
	}
	switch {
	case cfg.Shells.Mode == "off":
		add(1, "shell integration is off ([shell_integration] mode)", "prompt jumping and command marks need it")
	case sh == "fish" || sh == "zsh" || sh == "bash":
		add(0, "shell integration for "+sh, "")
	default:
		add(1, "no shell integration for "+sh, "fish, zsh and bash get prompt jumping and command marks")
	}
	if out, err := exec.Command("infocmp", "-x", "xterm-germ").CombinedOutput(); err != nil && !strings.Contains(string(out), "xterm-germ") {
		add(0, "terminfo xterm-germ: installed on first launch", "")
	}

	// Desktop
	if _, err := exec.LookPath("notify-send"); err != nil {
		add(1, "notify-send not found: no desktop notifications from agents", "install libnotify")
	} else {
		add(0, "desktop notifications (notify-send)", "")
	}
	if os.Getenv("BROWSER") == "" {
		if _, err := exec.LookPath("xdg-open"); err != nil {
			add(1, "xdg-open not found and $BROWSER unset: links won't open", "install xdg-utils")
		}
	}
	if os.Getenv("XDG_RUNTIME_DIR") == "" {
		add(1, "XDG_RUNTIME_DIR is unset: the control socket goes to /tmp", "")
	}
	if out, _ := exec.Command("xdg-mime", "query", "default", "x-scheme-handler/germ").Output(); !strings.Contains(string(out), "germ") {
		add(1, "germ:// links (Open in germ) aren't registered", "run: germ setup")
	} else {
		add(0, "germ:// links open germ", "")
	}

	// Agents
	if _, err := exec.LookPath("git"); err != nil {
		add(1, "git not found: Ctrl+Shift+A agents can't get their own worktrees", "install git")
	}
	if _, err := exec.LookPath("claude"); err == nil {
		data, _ := os.ReadFile(claudeSettingsPath())
		if strings.Contains(string(data), agentEventMarker) {
			add(0, "Claude Code reports its state to germ", "")
		} else {
			add(1, "Claude Code isn't set up to report its state (badges are guessed from the screen)", "run: germ setup agents")
		}
	}
	if _, err := exec.LookPath("codex"); err == nil {
		data, _ := os.ReadFile(codexConfigPath())
		if strings.Contains(string(data), "agent-event") {
			add(0, "Codex reports finished turns to germ", "")
		} else {
			add(1, "Codex isn't set up to report finished turns", "run: germ setup agents")
		}
	}

	// Sharing
	switch {
	case cfg.Share.Relay != "":
		if relayReachable(cfg.Share.Relay) {
			add(0, "sharing through relay "+cfg.Share.Relay, "")
		} else {
			add(2, "sharing relay "+cfg.Share.Relay+" doesn't answer: Ctrl+Shift+P can't share",
				"remove [share] relay from the config to share from this machine (free tunnel), or fix the relay")
		}
	case tailscaleFunnelReady():
		add(0, "sharing: Tailscale Funnel", "")
	default:
		if _, err := exec.LookPath("cloudflared"); err == nil {
			add(0, "sharing: Cloudflare quick tunnel (cloudflared)", "")
		} else {
			add(0, "sharing: Cloudflare quick tunnel (cloudflared is downloaded on first share)", "")
		}
	}

	// Crashes
	if fi, err := os.Stat(app.CrashLog()); err == nil && fi.Size() > 0 {
		add(1, "germ crashed on "+fi.ModTime().Format("Jan 2 15:04")+": "+app.CrashLog(),
			"please report it: https://github.com/zach-monroe/germ/issues")
	}

	worst := 0
	for _, c := range checks {
		mark := green + "✓" + reset
		switch c.level {
		case 1:
			mark = yel + "!" + reset
		case 2:
			mark = red + "✗" + reset
		}
		fmt.Printf("%s %s\n", mark, c.what)
		if c.hint != "" && c.level > 0 {
			fmt.Printf("  %s%s%s\n", dim, c.hint, reset)
		}
		worst = max(worst, c.level)
	}
	fmt.Printf("\ngerm %s\n", Version())
	if worst == 2 {
		return 1
	}
	return 0
}

var tmuxVersionRE = regexp.MustCompile(`(\d+)\.(\d+)`)

// tmuxAtLeast parses "tmux 3.4" / "tmux next-3.5" / "tmux 3.3a".
func tmuxAtLeast(v string, major, minor int) bool {
	m := tmuxVersionRE.FindStringSubmatch(v)
	if m == nil {
		return true // unknown format: probably a development build
	}
	a, _ := strconv.Atoi(m[1])
	b, _ := strconv.Atoi(m[2])
	return a > major || a == major && b >= minor
}

// relayReachable checks a relay's health endpoint.
func relayReachable(base string) bool {
	c := &http.Client{Timeout: 5 * time.Second}
	resp, err := c.Get(strings.TrimRight(base, "/") + "/healthz")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == 200
}

// tailscaleFunnelReady reports whether Tailscale is running and logged in.
func tailscaleFunnelReady() bool {
	out, err := exec.Command("tailscale", "status", "--json").Output()
	if err != nil {
		return false
	}
	var st struct {
		BackendState string
	}
	return json.Unmarshal(out, &st) == nil && st.BackendState == "Running"
}

// Command germ is a GPU-accelerated terminal emulator.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"runtime/pprof"
	"strings"
	"time"

	"github.com/zach-monroe/germ/internal/app"
	"github.com/zach-monroe/germ/internal/config"
	"github.com/zach-monroe/germ/internal/mcp"
	"github.com/zach-monroe/germ/internal/share"
	"github.com/zach-monroe/germ/internal/widgets"
)

func init() {
	// GLFW and OpenGL must be driven from the main OS thread.
	runtime.LockOSThread()
}

func main() {
	// `germ widget NAME` runs a built-in home-screen widget (germ starts
	// these itself in the home tab; running one by hand is handy for
	// testing).
	if len(os.Args) >= 2 && (os.Args[1] == "version" || os.Args[1] == "-version" || os.Args[1] == "--version") {
		fmt.Println("germ", Version())
		return
	}
	if len(os.Args) >= 2 && os.Args[1] == "share" {
		os.Exit(runShare(os.Args[2:]))
	}
	if len(os.Args) >= 3 && os.Args[1] == "ctl" && os.Args[2] == "agent-event" {
		os.Exit(runAgentEvent(os.Args[3:]))
	}
	if len(os.Args) >= 2 && os.Args[1] == "helm" {
		os.Exit(runHelm(os.Args[2:]))
	}
	if len(os.Args) >= 2 && os.Args[1] == "theme" {
		os.Exit(runTheme(os.Args[2:]))
	}
	if len(os.Args) >= 2 && os.Args[1] == "doctor" {
		os.Exit(runDoctor(os.Args[2:]))
	}
	if len(os.Args) >= 2 && os.Args[1] == "review" {
		os.Exit(runReview(os.Args[2:]))
	}
	if len(os.Args) >= 3 && os.Args[1] == "setup" && os.Args[2] == "agents" {
		os.Exit(runSetupAgents(os.Args[3:]))
	}
	if len(os.Args) >= 2 && os.Args[1] == "setup" {
		if err := ensureURLHandler(false); err != nil {
			fmt.Fprintln(os.Stderr, "germ setup:", err)
			os.Exit(1)
		}
		return
	}
	// `germ join LINK [flags]` opens a window on someone's shared session.
	joinLink := ""
	if len(os.Args) >= 3 && os.Args[1] == "join" {
		joinLink = os.Args[2]
		os.Args = append([]string{os.Args[0]}, os.Args[3:]...)
	}
	if len(os.Args) >= 2 && os.Args[1] == "agent" {
		os.Exit(runAgent(os.Args[2:]))
	}
	if len(os.Args) >= 2 && os.Args[1] == "ctl" {
		os.Exit(runCtl(os.Args[2:]))
	}
	if len(os.Args) >= 2 && os.Args[1] == "mcp" {
		if err := mcp.Serve(os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "germ mcp:", err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) >= 2 && os.Args[1] == "widget" {
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "usage: germ widget NAME   (built-in: "+strings.Join(widgets.Names(), ", ")+")")
			os.Exit(2)
		}
		if err := widgets.Run(os.Args[2]); err != nil {
			fmt.Fprintln(os.Stderr, "germ:", err)
			os.Exit(1)
		}
		return
	}

	configPath := flag.String("config", config.Path(), "config file")
	shell := flag.String("shell", "", "shell to run (overrides config; default $SHELL)")
	fontFamily := flag.String("font", "", "font family (overrides config)")
	fontSize := flag.Float64("font-size", 0, "font size in points (overrides config)")
	themeName := flag.String("theme", "", "theme name or path (overrides config)")
	tmuxFlag := flag.Bool("tmux", false, "attach to tmux in control mode even if disabled in the config")
	noTmux := flag.Bool("no-tmux", false, "run a plain shell instead of tmux")
	session := flag.String("session", "", "tmux session to attach to or create (default from config, else a free germ-N session)")
	listThemesFlag := flag.Bool("list-themes", false, "list themes and exit (same as germ theme list)")
	quote := flag.Bool("quote", false, "log child output as quoted strings to show escape sequences")
	shot := flag.String("screenshot", "", "save a PNG of the window to this path after -screenshot-delay, then exit (testing)")
	shotDelay := flag.Duration("screenshot-delay", time.Second, "delay before -screenshot")
	dump := flag.Bool("dump", false, "print the terminal screen as text when the session ends")
	flag.Parse()

	if *listThemesFlag {
		listThemes()
		return
	}

	app.RecordCrashes()
	defer app.RemoveCrashTemp()
	cfg, warnings, err := config.Load(*configPath)
	problems := warnings
	if err != nil {
		problems = append(problems, fmt.Sprintf("%v (using the defaults)", err))
	}

	// Explicit flags win over the config file, including across reloads.
	var over app.Overrides
	flag.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "font":
			over.FontFamily = *fontFamily
		case "font-size":
			over.FontSize = *fontSize
		case "theme":
			over.Theme = *themeName
		}
	})

	sh := *shell
	if sh == "" {
		sh = cfg.Shell.Program
	}
	if sh == "" {
		sh = os.Getenv("SHELL")
	}
	if sh == "" {
		sh = "/bin/sh"
	}
	shellCmd := append([]string{sh}, cfg.Shell.Args...)
	cmd := flag.Args()
	if joinLink == "" && len(cmd) >= 2 && cmd[0] == "join" { // germ [flags] join LINK
		joinLink, cmd = cmd[1], nil
	}
	if len(cmd) == 0 {
		cmd = shellCmd
	}

	if path := os.Getenv("GERM_CPUPROFILE"); path != "" {
		f, err := os.Create(path)
		if err == nil {
			pprof.StartCPUProfile(f)
			defer pprof.StopCPUProfile()
		}
	}

	// tmux is the default; an explicit command runs without it.
	useTmux := (*tmuxFlag || *session != "" || cfg.Tmux.Enabled) && !*noTmux && (len(flag.Args()) == 0 || joinLink != "")
	tmuxSession := cfg.Tmux.Session
	if *session != "" {
		tmuxSession = *session
	}

	var remote *share.Guest
	if joinLink != "" {
		g, err := prepareJoin(joinLink)
		if err != nil {
			fmt.Fprintln(os.Stderr, "germ join:", err)
			if p, lerr := exec.LookPath("notify-send"); lerr == nil { // launched from a browser: no terminal
				exec.Command(p, "--app-name=germ", "germ: couldn't join", err.Error()).Run()
			}
			os.Exit(1)
		}
		remote = g
		useTmux = false
	}

	var tmuxCmd []string
	if *shell != "" {
		tmuxCmd = shellCmd
	}
	if err := app.Run(app.Options{
		Command: cmd, ShellCommand: shellCmd, TmuxCommand: tmuxCmd, ConfigProblems: problems, Out: os.Stdout, Quote: *quote, Dump: *dump,
		Config: cfg, ConfigPath: *configPath, Overrides: over, ExplicitCommand: len(flag.Args()) > 0 && joinLink == "", UseTmux: useTmux, TmuxSession: tmuxSession, Remote: remote,
		Screenshot: *shot, ScreenshotDelay: *shotDelay,
	}); err != nil {
		fmt.Fprintln(os.Stderr, "germ:", err)
		pprof.StopCPUProfile()
		os.Exit(1)
	}
}

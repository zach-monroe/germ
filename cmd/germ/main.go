// Command germ is a GPU-accelerated terminal emulator.
package main

import (
	"flag"
	"fmt"
	"os"
	"runtime"
	"runtime/pprof"
	"strings"
	"time"

	"github.com/zach-monroe/germ/internal/app"
	"github.com/zach-monroe/germ/internal/config"
	"github.com/zach-monroe/germ/internal/theme"
)

func init() {
	// GLFW and OpenGL must be driven from the main OS thread.
	runtime.LockOSThread()
}

func main() {
	configPath := flag.String("config", config.Path(), "config file")
	shell := flag.String("shell", "", "shell to run (overrides config; default $SHELL)")
	fontFamily := flag.String("font", "", "font family (overrides config)")
	fontSize := flag.Float64("font-size", 0, "font size in points (overrides config)")
	themeName := flag.String("theme", "", "theme name or path (overrides config)")
	tmuxFlag := flag.Bool("tmux", false, "attach to tmux in control mode (tabs and splits become tmux windows and panes)")
	noTmux := flag.Bool("no-tmux", false, "don't use tmux even if enabled in the config")
	session := flag.String("session", "", "tmux session name for -tmux (default from config, else \"main\")")
	listThemes := flag.Bool("list-themes", false, "list bundled themes and exit")
	quote := flag.Bool("quote", false, "log child output as quoted strings to show escape sequences")
	shot := flag.String("screenshot", "", "save a PNG of the window to this path after -screenshot-delay, then exit (testing)")
	shotDelay := flag.Duration("screenshot-delay", time.Second, "delay before -screenshot")
	dump := flag.Bool("dump", false, "print the terminal screen as text when the session ends")
	flag.Parse()

	if *listThemes {
		fmt.Println(strings.Join(theme.Bundled(), "\n"))
		return
	}

	cfg, warnings, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "germ: %s: %v (using defaults)\n", *configPath, err)
	}
	for _, w := range warnings {
		fmt.Fprintf(os.Stderr, "germ: %s: %s\n", *configPath, w)
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

	tmuxSession := ""
	if (*tmuxFlag || cfg.Tmux.Enabled) && !*noTmux && len(flag.Args()) == 0 {
		tmuxSession = cfg.Tmux.Session
		if *session != "" {
			tmuxSession = *session
		}
	}

	if err := app.Run(app.Options{
		Command: cmd, ShellCommand: shellCmd, Out: os.Stdout, Quote: *quote, Dump: *dump,
		Config: cfg, ConfigPath: *configPath, Overrides: over, Tmux: tmuxSession,
		Screenshot: *shot, ScreenshotDelay: *shotDelay,
	}); err != nil {
		fmt.Fprintln(os.Stderr, "germ:", err)
		pprof.StopCPUProfile()
		os.Exit(1)
	}
}

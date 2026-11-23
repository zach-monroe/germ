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

	cmd := flag.Args()
	if len(cmd) == 0 {
		s := *shell
		if s == "" {
			s = cfg.Shell.Program
		}
		if s == "" {
			s = os.Getenv("SHELL")
		}
		if s == "" {
			s = "/bin/sh"
		}
		cmd = append([]string{s}, cfg.Shell.Args...)
	}

	if path := os.Getenv("GERM_CPUPROFILE"); path != "" {
		f, err := os.Create(path)
		if err == nil {
			pprof.StartCPUProfile(f)
			defer pprof.StopCPUProfile()
		}
	}

	if err := app.Run(app.Options{
		Command: cmd, Out: os.Stdout, Quote: *quote, Dump: *dump,
		Config: cfg, ConfigPath: *configPath, Overrides: over,
		Screenshot: *shot, ScreenshotDelay: *shotDelay,
	}); err != nil {
		fmt.Fprintln(os.Stderr, "germ:", err)
		pprof.StopCPUProfile()
		os.Exit(1)
	}
}

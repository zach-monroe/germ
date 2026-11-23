// Command germ is a GPU-accelerated terminal emulator.
package main

import (
	"flag"
	"fmt"
	"os"
	"runtime"
	"runtime/pprof"
	"time"

	"github.com/zach-monroe/germ/internal/app"
)

func init() {
	// GLFW and OpenGL must be driven from the main OS thread.
	runtime.LockOSThread()
}

func main() {
	shell := flag.String("shell", "", "shell to run (default: $SHELL, then /bin/sh)")
	quote := flag.Bool("quote", false, "log child output as quoted strings to show escape sequences")
	fontFamily := flag.String("font", "monospace", "font family (fontconfig name)")
	fontSize := flag.Float64("font-size", 12, "font size in points")
	shot := flag.String("screenshot", "", "save a PNG of the window to this path after -screenshot-delay, then exit (testing)")
	shotDelay := flag.Duration("screenshot-delay", time.Second, "delay before -screenshot")
	dump := flag.Bool("dump", false, "print the terminal screen as text when the session ends")
	flag.Parse()

	cmd := flag.Args()
	if len(cmd) == 0 {
		s := *shell
		if s == "" {
			s = os.Getenv("SHELL")
		}
		if s == "" {
			s = "/bin/sh"
		}
		cmd = []string{s}
	}

	if path := os.Getenv("GERM_CPUPROFILE"); path != "" {
		f, err := os.Create(path)
		if err == nil {
			pprof.StartCPUProfile(f)
			defer pprof.StopCPUProfile()
		}
	}

	if err := app.Run(app.Options{Command: cmd, Out: os.Stdout, Quote: *quote, Dump: *dump,
		FontFamily: *fontFamily, FontSize: *fontSize,
		Screenshot: *shot, ScreenshotDelay: *shotDelay}); err != nil {
		fmt.Fprintln(os.Stderr, "germ:", err)
		pprof.StopCPUProfile()
		os.Exit(1)
	}
}

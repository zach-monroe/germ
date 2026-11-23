// Command germ is a GPU-accelerated terminal emulator.
package main

import (
	"flag"
	"fmt"
	"os"
	"runtime"

	"github.com/zach-monroe/germ/internal/app"
)

func init() {
	// GLFW and OpenGL must be driven from the main OS thread.
	runtime.LockOSThread()
}

func main() {
	shell := flag.String("shell", "", "shell to run (default: $SHELL, then /bin/sh)")
	quote := flag.Bool("quote", false, "log child output as quoted strings to show escape sequences")
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

	if err := app.Run(app.Options{Command: cmd, Out: os.Stdout, Quote: *quote}); err != nil {
		fmt.Fprintln(os.Stderr, "germ:", err)
		os.Exit(1)
	}
}

// Package pty spawns a child process attached to a pseudo-terminal.
package pty

import (
	"fmt"
	"os"
	"os/exec"

	cpty "github.com/creack/pty"
)

// Session is a running child process and the master side of its PTY.
type Session struct {
	f   *os.File
	cmd *exec.Cmd
}

// Start launches argv[0] with the given args on a new PTY sized cols x rows.
// extraEnv entries are added to (and override) the inherited environment.
func Start(argv []string, cols, rows uint16, extraEnv ...string) (*Session, error) {
	return StartIn("", argv, cols, rows, extraEnv...)
}

// StartIn is Start with a working directory ("" = inherit).
func StartIn(dir string, argv []string, cols, rows uint16, extraEnv ...string) (*Session, error) {
	if len(argv) == 0 {
		return nil, fmt.Errorf("pty: empty command")
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"TERM=xterm-256color", // callers may override with TERM=germ
		"COLORTERM=truecolor",
		"TERM_PROGRAM=germ",
	)
	cmd.Env = append(cmd.Env, extraEnv...)
	f, err := cpty.StartWithSize(cmd, &cpty.Winsize{Cols: cols, Rows: rows})
	if err != nil {
		return nil, fmt.Errorf("pty: start %q: %w", argv[0], err)
	}
	return &Session{f: f, cmd: cmd}, nil
}

// Read reads child output from the PTY master.
func (s *Session) Read(p []byte) (int, error) { return s.f.Read(p) }

// Write sends input bytes to the child.
func (s *Session) Write(p []byte) (int, error) { return s.f.Write(p) }

// Resize informs the child of a new window size (TIOCSWINSZ + SIGWINCH).
func (s *Session) Resize(cols, rows uint16) error {
	return cpty.Setsize(s.f, &cpty.Winsize{Cols: cols, Rows: rows})
}

// Wait blocks until the child exits.
func (s *Session) Wait() error { return s.cmd.Wait() }

// Cwd returns the child's current directory (Linux /proc), or "".
func (s *Session) Cwd() string {
	if s.cmd.Process == nil {
		return ""
	}
	d, err := os.Readlink(fmt.Sprintf("/proc/%d/cwd", s.cmd.Process.Pid))
	if err != nil {
		return ""
	}
	return d
}

// Pid returns the child's process ID.
func (s *Session) Pid() int { return s.cmd.Process.Pid }

// Close closes the PTY master and kills the child if it is still running.
func (s *Session) Close() error {
	if s.cmd.ProcessState == nil && s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
	return s.f.Close()
}

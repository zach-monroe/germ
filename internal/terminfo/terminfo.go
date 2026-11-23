// Package terminfo installs germ's terminfo entry so programs can use
// TERM=germ and learn about its extended capabilities (true color, styled
// underlines, synchronized output, clipboard, focus events).
package terminfo

import (
	"bytes"
	_ "embed"
	"os"
	"os/exec"
	"path/filepath"
)

//go:embed germ.ti
var source []byte

// Name is the terminfo entry name.
const Name = "xterm-germ"

// Env compiles the terminfo entry if needed and returns environment
// variables selecting it. If installation fails (e.g. no tic), it returns
// nil and the caller keeps TERM=xterm-256color.
func Env() []string {
	dir, err := install()
	if err != nil {
		return nil
	}
	dirs := dir + ":" // trailing empty element = the system default locations
	if cur := os.Getenv("TERMINFO_DIRS"); cur != "" {
		dirs = dir + ":" + cur
	}
	return []string{"TERM=" + Name, "TERMINFO_DIRS=" + dirs}
}

func install() (string, error) {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".local", "share")
	}
	dir := filepath.Join(base, "germ", "terminfo")
	src := filepath.Join(dir, "germ.ti")
	compiled := filepath.Join(dir, "x", "xterm-germ")
	if old, err := os.ReadFile(src); err == nil && bytes.Equal(old, source) {
		if _, err := os.Stat(compiled); err == nil {
			return dir, nil // up to date
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(src, source, 0o644); err != nil {
		return "", err
	}
	if out, err := exec.Command("tic", "-x", "-o", dir, src).CombinedOutput(); err != nil {
		os.Remove(src) // retry next time
		return "", &ticError{err, out}
	}
	return dir, nil
}

type ticError struct {
	err error
	out []byte
}

func (e *ticError) Error() string { return "tic: " + e.err.Error() + ": " + string(e.out) }

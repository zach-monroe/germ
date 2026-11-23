// Package shellint installs germ's shell integration scripts and prepares
// the environment so supported shells load them automatically.
package shellint

import (
	"bytes"
	"embed"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

//go:embed all:scripts
var scripts embed.FS

// Env returns environment variables to add for the given command so its
// shell picks up the integration, or nil if the shell isn't supported or
// installation fails. fish, zsh, and bash are supported.
func Env(argv []string) []string {
	if len(argv) == 0 {
		return nil
	}
	switch filepath.Base(argv[0]) {
	case "fish":
		return envFor(fishEnv)
	case "zsh":
		return envFor(zshEnv)
	case "bash":
		return envFor(bashEnv)
	}
	return nil
}

// AllEnv is for shells germ doesn't start itself (tmux panes run whatever
// shell tmux is configured with): every shell's injection at once. Each
// shell's script undoes the others'.
func AllEnv() []string { return envFor(fishEnv, zshEnv, bashEnv) }

func envFor(fns ...func(dir string) []string) []string {
	dir, err := install()
	if err != nil {
		return nil
	}
	var env []string
	for _, fn := range fns {
		env = append(env, fn(dir)...)
	}
	return env
}

// fish loads vendor_conf.d scripts from XDG_DATA_DIRS.
func fishEnv(dir string) []string {
	orig, ok := os.LookupEnv("XDG_DATA_DIRS")
	cur := orig
	if !ok || orig == "" {
		cur = "/usr/local/share:/usr/share" // the XDG default
		if !ok {
			orig = "__unset__"
		}
	}
	return []string{
		"XDG_DATA_DIRS=" + dir + ":" + cur,
		"GERM_ORIG_XDG_DATA_DIRS=" + orig,
	}
}

// zsh reads $ZDOTDIR/.zshenv first; germ's puts ZDOTDIR back.
func zshEnv(dir string) []string {
	orig, ok := os.LookupEnv("ZDOTDIR")
	if !ok {
		orig = "__unset__"
	}
	return []string{"ZDOTDIR=" + filepath.Join(dir, "zsh"), "GERM_ORIG_ZDOTDIR=" + orig}
}

// bash runs PROMPT_COMMAND before the first prompt; germ's loads the
// integration, which then replaces it.
func bashEnv(dir string) []string {
	if _, ok := os.LookupEnv("PROMPT_COMMAND"); ok {
		return nil // don't clobber one the user exported
	}
	return []string{"PROMPT_COMMAND=source " + shQuote(filepath.Join(dir, "bash", "germ-integration.bash"))}
}

func shQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// install writes the embedded scripts under the user's data directory
// (rewriting them only when they changed) and returns that directory.
func install() (string, error) {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".local", "share")
	}
	root := filepath.Join(base, "germ", "shell-integration")
	err := fs.WalkDir(scripts, "scripts", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := scripts.ReadFile(path)
		if err != nil {
			return err
		}
		dst := filepath.Join(root, strings.TrimPrefix(path, "scripts/"))
		if old, err := os.ReadFile(dst); err == nil && bytes.Equal(old, data) {
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		tmp := dst + ".tmp"
		if err := os.WriteFile(tmp, data, 0o644); err != nil {
			return err
		}
		return os.Rename(tmp, dst)
	})
	return root, err
}

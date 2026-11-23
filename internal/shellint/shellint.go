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

//go:embed scripts
var scripts embed.FS

// Env returns environment variables to add for the given command so its
// shell picks up the integration, or nil if the shell isn't supported or
// installation fails.
func Env(argv []string) []string {
	if len(argv) == 0 || filepath.Base(argv[0]) != "fish" {
		return nil
	}
	dir, err := install()
	if err != nil {
		return nil
	}
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

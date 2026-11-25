package config

import (
	"path/filepath"
	"slices"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/zach-monroe/germ/internal/theme"
)

// Watch calls onChange (from a background goroutine) whenever the config
// file (path, or the default one if empty), a user theme, or the file the
// configured theme is read from changes. That file can live anywhere: a
// theme given as a path, or a theme linked to a file a desktop theme tool
// rewrites. Directories are watched rather than files
// because editors usually save by writing a new file and renaming it over
// the old one. Events are debounced. The returned function stops watching.
func Watch(path string, onChange func()) (stop func(), err error) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	if path == "" {
		path = Path()
	}
	path, _ = filepath.Abs(path)
	configFiles := withTarget(path) // a dotfiles link: the file it points to changes
	var files, dirs []string
	// track recomputes the files that matter, since the theme can change,
	// and watches their directories.
	track := func() (watched int) {
		files = append(slices.Clone(configFiles), themeFiles(path)...)
		dirs = append([]string{Dir()}, ThemeDirs()...)
		for _, f := range files {
			if d := filepath.Dir(f); !slices.Contains(dirs, d) {
				dirs = append(dirs, d)
			}
		}
		for _, d := range dirs {
			if w.Add(d) == nil {
				watched++
			}
		}
		return watched
	}
	watched := track()
	// If the config dir doesn't exist yet, watch its parent so creating it
	// is noticed too.
	if watched == 0 {
		_ = w.Add(filepath.Dir(Dir()))
	}

	done := make(chan struct{})
	go func() {
		var timer *time.Timer
		for {
			select {
			case <-done:
				return
			case ev, ok := <-w.Events:
				if !ok {
					return
				}
				if !relevant(ev.Name, files) {
					continue
				}
				track()
				if timer != nil {
					timer.Stop()
				}
				timer = time.AfterFunc(100*time.Millisecond, onChange)
			case <-w.Errors:
			}
		}
	}()
	return func() { close(done); w.Close() }, nil
}

func relevant(name string, files []string) bool {
	if name == Dir() || slices.Contains(files, name) {
		return true
	}
	for _, d := range ThemeDirs() {
		if filepath.Dir(name) == d || name == d {
			return filepath.Ext(name) == ".toml" || name == d
		}
	}
	return false
}

// withTarget returns the absolute path p, and the file it links to if it
// is a symlink.
func withTarget(p string) []string {
	p, _ = filepath.Abs(p)
	files := []string{p}
	if t, err := filepath.EvalSymlinks(p); err == nil && t != p {
		files = append(files, t)
	}
	return files
}

// themeFiles returns the file the theme configured at path is read from,
// and its link target, or nothing for a bundled theme.
func themeFiles(path string) []string {
	cfg, _, err := Load(path)
	if err != nil {
		return nil
	}
	f := theme.Path(cfg.Theme, ThemeDirs()...)
	if f == "" {
		return nil
	}
	return withTarget(f)
}

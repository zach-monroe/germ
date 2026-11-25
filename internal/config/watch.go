package config

import (
	"path/filepath"
	"slices"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Watch calls onChange (from a background goroutine) whenever the config
// file (path, or the default one if empty) or a user theme changes. Directories are watched rather than files
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
	files := []string{path}
	if p, err := filepath.EvalSymlinks(path); err == nil && p != path {
		files = append(files, p) // a dotfiles link: the file it points to changes
	}
	dirs := []string{Dir()}
	for _, f := range files {
		dirs = append(dirs, filepath.Dir(f))
	}
	dirs = append(dirs, ThemeDirs()...)
	watched := 0
	for _, d := range dirs {
		if w.Add(d) == nil {
			watched++
		}
	}
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
				if ev.Op&fsnotify.Create != 0 && (ev.Name == Dir() || slices.Contains(ThemeDirs(), ev.Name)) {
					for _, d := range dirs {
						_ = w.Add(d)
					}
				}
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

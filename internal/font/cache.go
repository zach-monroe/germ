package font

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// A disk cache of fc-match answers, shared by germ windows: each lookup
// costs ~20 ms of process startup, which adds up at launch and stalls a
// frame the first time an unusual character needs a fallback font. The
// cache is dropped whenever fontconfig's own caches are newer (fonts were
// installed or removed), and entries whose file is gone are ignored.

type cachedMatch struct {
	Path  string `json:"path"`
	Index int    `json:"index"`
}

type fontCache struct {
	once    sync.Once
	mu      sync.Mutex
	path    string
	entries map[string]cachedMatch
	dirty   bool
	saving  bool
}

var matchCache = &fontCache{}

func (c *fontCache) load() {
	c.entries = map[string]cachedMatch{}
	base := os.Getenv("XDG_CACHE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return
		}
		base = filepath.Join(home, ".cache")
	}
	c.path = filepath.Join(base, "germ", "fontmatch.json")
	fi, err := os.Stat(c.path)
	if err != nil || fontconfigChangedSince(fi.ModTime(), base) {
		return
	}
	data, err := os.ReadFile(c.path)
	if err != nil {
		return
	}
	json.Unmarshal(data, &c.entries)
	if c.entries == nil {
		c.entries = map[string]cachedMatch{}
	}
}

// fontconfigChangedSince reports whether fontconfig's caches (rebuilt when
// fonts change) are newer than t.
func fontconfigChangedSince(t time.Time, cacheBase string) bool {
	for _, dir := range []string{filepath.Join(cacheBase, "fontconfig"), "/var/cache/fontconfig"} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if fi, err := e.Info(); err == nil && fi.ModTime().After(t) {
				return true
			}
		}
	}
	return false
}

func (c *fontCache) get(pattern string) (cachedMatch, bool) {
	c.once.Do(c.load)
	c.mu.Lock()
	m, ok := c.entries[pattern]
	c.mu.Unlock()
	if !ok {
		return m, false
	}
	if _, err := os.Stat(m.Path); err != nil {
		return m, false // the font file went away
	}
	return m, true
}

func (c *fontCache) put(pattern string, m cachedMatch) {
	c.once.Do(c.load)
	c.mu.Lock()
	c.entries[pattern] = m
	c.dirty = true
	start := !c.saving && c.path != ""
	c.saving = c.saving || start
	c.mu.Unlock()
	if start {
		// Write a moment later, so a burst of lookups (startup) is one write.
		time.AfterFunc(200*time.Millisecond, c.save)
	}
}

func (c *fontCache) save() {
	c.mu.Lock()
	data, err := json.Marshal(c.entries)
	c.dirty, c.saving = false, false
	path := c.path
	c.mu.Unlock()
	if err != nil {
		return
	}
	os.MkdirAll(filepath.Dir(path), 0o755)
	tmp := path + ".tmp"
	if os.WriteFile(tmp, data, 0o644) == nil {
		os.Rename(tmp, path)
	}
}

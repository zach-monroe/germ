package term

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/zach-monroe/germ/internal/theme"
)

// Dynamic colors: the effective palette is the theme plus any overrides
// applications set with OSC 4/10/11/12. Overrides survive theme reloads
// and are cleared by OSC 104/110/111/112 or a full reset. With fixed set,
// programs can still ask for colors but not change them.

const (
	dynFG = iota
	dynBG
	dynCursor
)

type colorState struct {
	base *theme.Theme
	eff  theme.Theme
	pal  map[int]theme.RGB
	dyn  [3]*theme.RGB
	gen  uint64 // bumped on every change so renderers can rebuild
	// fixed ignores programs' color changes: the theme always shows.
	fixed bool
}

func (cs *colorState) recompute() {
	cs.eff = *cs.base
	for i, c := range cs.pal {
		cs.eff.Palette[i] = c
	}
	if c := cs.dyn[dynFG]; c != nil {
		cs.eff.Foreground = *c
	}
	if c := cs.dyn[dynBG]; c != nil {
		cs.eff.Background = *c
	}
	if c := cs.dyn[dynCursor]; c != nil {
		cs.eff.Cursor = *c
	}
	cs.gen++
}

func (cs *colorState) reset() {
	cs.pal = nil
	cs.dyn = [3]*theme.RGB{}
	cs.recompute()
}

// SetTheme replaces the base theme (e.g. after a config reload).
func (t *Terminal) SetTheme(th *theme.Theme) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.colors.base = th
	t.colors.recompute()
	t.markAllDirty()
}

// SetFixedColors makes the theme win over colors programs set (OSC
// 4/10/11/12); turning it on drops any they already set.
func (t *Terminal) SetFixedColors(on bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	cs := &t.colors
	if cs.fixed == on {
		return
	}
	cs.fixed = on
	if on && (cs.pal != nil || cs.dyn != [3]*theme.RGB{}) {
		cs.reset()
		t.markAllDirty()
	}
}

// Colors returns the effective color scheme and a generation number that
// changes whenever it does. Caller must hold the lock; the returned
// pointer is valid until Unlock.
func (t *Terminal) Colors() (*theme.Theme, uint64) {
	return &t.colors.eff, t.colors.gen
}

func rgbSpec(c theme.RGB) string {
	return fmt.Sprintf("rgb:%04x/%04x/%04x", uint16(c.R)*257, uint16(c.G)*257, uint16(c.B)*257)
}

// oscColor handles OSC 4, 10–12, 104, and 110–112.
func (t *Terminal) oscColor(code int, rest string) {
	cs := &t.colors
	changed := false
	switch {
	case code == 4: // OSC 4 ; index ; spec [; index ; spec ...]
		parts := strings.Split(rest, ";")
		for i := 0; i+1 < len(parts); i += 2 {
			idx, err := strconv.Atoi(parts[i])
			if err != nil || idx < 0 || idx > 255 {
				continue
			}
			if parts[i+1] == "?" {
				t.respond(fmt.Sprintf("\x1b]4;%d;%s\x1b\\", idx, rgbSpec(cs.eff.Palette[idx])))
				continue
			}
			if c, err := theme.ParseColor(parts[i+1]); err == nil && !cs.fixed {
				if cs.pal == nil {
					cs.pal = map[int]theme.RGB{}
				}
				cs.pal[idx] = c
				changed = true
			}
		}
	case code >= 10 && code <= 12: // OSC 10 ; spec [; spec ...] (applies to 10, 11, 12 in turn)
		for i, spec := range strings.Split(rest, ";") {
			which := code - 10 + i
			if which > dynCursor {
				break
			}
			if spec == "?" {
				cur := [3]theme.RGB{cs.eff.Foreground, cs.eff.Background, cs.eff.Cursor}[which]
				t.respond(fmt.Sprintf("\x1b]%d;%s\x1b\\", 10+which, rgbSpec(cur)))
				continue
			}
			if c, err := theme.ParseColor(spec); err == nil && !cs.fixed {
				cs.dyn[which] = &c
				changed = true
			}
		}
	case code == 104:
		if rest == "" {
			cs.pal = nil
		} else {
			for _, s := range strings.Split(rest, ";") {
				if idx, err := strconv.Atoi(s); err == nil {
					delete(cs.pal, idx)
				}
			}
		}
		changed = true
	case code >= 110 && code <= 112:
		cs.dyn[code-110] = nil
		changed = true
	}
	if changed {
		cs.recompute()
		t.markAllDirty()
	}
}

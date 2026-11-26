// Package widgets implements germ's built-in home-screen widgets.
//
// A widget is just a program that draws into its terminal: germ runs it in
// a PTY sized to the widget's box on the home tab. The built-in widgets
// are subcommands of the germ binary (`germ widget clock`) and use the same
// protocol as user widgets:
//
//   - the terminal size is the widget's box (resize arrives as SIGWINCH);
//   - options from the config arrive as GERM_WIDGET_<KEY> environment
//     variables, e.g. location = "Denver" -> GERM_WIDGET_LOCATION=Denver;
//   - colors should use the 16 ANSI palette colors so the theme applies.
package widgets

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

// Widget draws one frame for a w×h box. It's called again on resize, on
// every tick, and whenever its data changes.
type Widget interface {
	// Update refreshes data in the background; it's called once at start
	// and then every Every(). It may take a while (network).
	Update(ctx context.Context) error
	Every() time.Duration
	// Draw renders the current state. err is the last Update error.
	Draw(w, h int, err error) []string
	// Tick is how often to redraw without new data (0: only on changes).
	Tick() time.Duration
}

// Clicker is a widget that takes clicks: it draws click(token) links, and
// a click on one calls Click with its token, then redraws.
type Clicker interface {
	Click(ctx context.Context, token string)
}

// click is a link that sends token back to the widget when clicked.
func click(token, text string) string { return link("germ-widget:"+token, text) }

var builtins = map[string]func(Options) Widget{
	"clock":   newClock,
	"weather": newWeather,
	"news":    newNews,
	"stocks":  newStocks,
	"agents":  newAgents,
	"git":     newGit,
}

// Names lists the built-in widgets.
func Names() []string {
	var out []string
	for n := range builtins {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// IsBuiltin reports whether name is a built-in widget.
func IsBuiltin(name string) bool { _, ok := builtins[name]; return ok }

// Run runs a built-in widget on stdout until killed (`germ widget NAME`):
// options come from GERM_WIDGET_* variables and resizes from SIGWINCH.
func Run(name string) error {
	resize := make(chan struct{}, 1)
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	go func() {
		for range winch {
			select {
			case resize <- struct{}{}:
			default:
			}
		}
	}()
	return RunInProcess(context.Background(), name, envOptions(), os.Stdout, ttySize, resize, nil, nil)
}

// Shown says whether a widget is on screen. A hidden widget is dormant: it
// doesn't refresh its data, tick or draw. Shown again, it draws its last
// state at once and refreshes its data if that's due. A nil *Shown is
// always shown.
type Shown struct {
	mu      sync.Mutex
	on      bool
	changed chan struct{} // closed when on changes
}

// NewShown returns a Shown that starts on or off.
func NewShown(on bool) *Shown { return &Shown{on: on, changed: make(chan struct{})} }

// Set shows or hides the widget.
func (s *Shown) Set(on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.on != on {
		s.on = on
		close(s.changed)
		s.changed = make(chan struct{})
	}
}

// Get reports whether the widget is shown, and a channel closed when that
// changes.
func (s *Shown) Get() (bool, <-chan struct{}) {
	if s == nil {
		return true, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.on, s.changed
}

// WaitShown blocks until the widget is shown; false if ctx ends first.
func (s *Shown) WaitShown(ctx context.Context) bool {
	for {
		on, changed := s.Get()
		if on {
			return true
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return false
		}
	}
}

// waitDue blocks until the widget is shown and data fetched at last is
// every old (at once if last is zero); false if ctx ends first.
func (s *Shown) waitDue(ctx context.Context, last time.Time, every time.Duration) bool {
	for {
		if !s.WaitShown(ctx) {
			return false
		}
		wait := time.Until(last.Add(every))
		if last.IsZero() || wait <= 0 {
			return true
		}
		_, changed := s.Get()
		t := time.NewTimer(wait)
		select {
		case <-t.C: // loop: check it's still shown
		case <-changed:
			t.Stop()
		case <-ctx.Done():
			t.Stop()
			return false
		}
	}
}

// RunInProcess runs a built-in widget, drawing frames to out, until ctx
// ends. size reports the widget's box; a value on resize redraws. germ
// runs its home-screen widgets this way, without a process each, and
// hides them (shown) while the home tab isn't on screen. Tokens on clicks
// go to the widget's Click method, if it has one.
func RunInProcess(ctx context.Context, name string, opts Options, out io.Writer,
	size func() (cols, rows int), resize <-chan struct{}, shown *Shown, clicks <-chan string) error {
	mk, ok := builtins[name]
	if !ok {
		return fmt.Errorf("unknown widget %q (built-in: %s)", name, strings.Join(Names(), ", "))
	}
	w := mk(opts)
	io.WriteString(out, "\x1b[?25l\x1b[?7l") // no cursor, no autowrap

	updated := make(chan error, 1)
	go func() {
		var last time.Time
		for {
			if !last.IsZero() && w.Every() <= 0 {
				return // fetches once
			}
			if !shown.waitDue(ctx, last, w.Every()) {
				return
			}
			last = time.Now()
			uctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			err := w.Update(uctx)
			cancel()
			select {
			case updated <- err:
			case <-ctx.Done():
				return
			}
		}
	}()

	var tick <-chan time.Time
	if d := w.Tick(); d > 0 {
		ch := make(chan time.Time, 1)
		tick = ch
		go runTicker(ctx, d, shown, ch)
	}
	var lastErr error
	dirty := true
	for {
		on, changed := shown.Get()
		if on && dirty {
			cols, rows := size()
			draw(out, w.Draw(cols, rows, lastErr), rows)
			dirty = false
		}
		select {
		case <-resize:
		case <-tick:
		case lastErr = <-updated:
		case <-changed: // shown again: redraw (a clock's time moved on)
		case tok := <-clicks:
			if c, ok := w.(Clicker); ok {
				c.Click(ctx, tok)
			}
		case <-ctx.Done():
			return nil
		}
		dirty = true
	}
}

// runTicker sends on ch every d while the widget is shown, aligned to the
// period so a clock flips on the second.
func runTicker(ctx context.Context, d time.Duration, shown *Shown, ch chan<- time.Time) {
	for shown.WaitShown(ctx) {
		_, changed := shown.Get()
		first := time.NewTimer(time.Until(time.Now().Truncate(d).Add(d)))
		var t *time.Ticker
		var tc <-chan time.Time
	ticking:
		for {
			select {
			case now := <-first.C:
				t = time.NewTicker(d)
				tc = t.C
				select {
				case ch <- now:
				default:
				}
			case now := <-tc:
				select {
				case ch <- now:
				default:
				}
			case <-changed:
				break ticking
			case <-ctx.Done():
				break ticking
			}
		}
		first.Stop()
		if t != nil {
			t.Stop()
		}
	}
}

func ttySize() (int, int) {
	ws, err := unix.IoctlGetWinsize(1, unix.TIOCGWINSZ)
	if err != nil || ws.Col == 0 {
		return 40, 10
	}
	return int(ws.Col), int(ws.Row)
}

// Options are a widget's settings ([widgets.NAME] keys).
type Options map[string]string

// Get returns an option, or def if it's unset.
func (o Options) Get(key, def string) string {
	if v := o[strings.ToLower(key)]; v != "" {
		return v
	}
	return def
}

// envOptions reads GERM_WIDGET_<KEY> variables (how separate widget
// processes get their options).
func envOptions() Options {
	o := Options{}
	for _, kv := range os.Environ() {
		k, v, _ := strings.Cut(kv, "=")
		if rest, ok := strings.CutPrefix(k, "GERM_WIDGET_"); ok {
			o[strings.ToLower(rest)] = v
		}
	}
	return o
}

// draw writes a frame in one synchronized update.
func draw(out io.Writer, lines []string, rows int) {
	var b strings.Builder
	b.WriteString("\x1b[?2026h\x1b[H")
	for i, l := range lines {
		if i >= rows {
			break
		}
		if i > 0 {
			b.WriteString("\r\n")
		}
		// Clear first: an erase after a full-width line would remove
		// its last character.
		b.WriteString("\x1b[2K")
		b.WriteString(l)
		b.WriteString("\x1b[0m")
	}
	b.WriteString("\x1b[0m")
	if len(lines) < rows {
		// Clear below. (Not from the end of a full last row, which would
		// erase its final character.)
		b.WriteString("\r\n\x1b[J")
	}
	b.WriteString("\x1b[?2026l")
	io.WriteString(out, b.String())
}

// ---- helpers shared by the built-ins ------------------------------------------

// SGR color helpers using the theme's ANSI palette.
const (
	reset   = "\x1b[0m"
	bold    = "\x1b[1m"
	dim     = "\x1b[2m"
	red     = "\x1b[31m"
	green   = "\x1b[32m"
	yellow  = "\x1b[33m"
	blue    = "\x1b[34m"
	magenta = "\x1b[35m"
	cyan    = "\x1b[36m"
	white   = "\x1b[37m"
	bright  = "\x1b[97m"
)

// title is a widget header: an icon and name on the left, info on the right.
func title(w int, icon, name, info string) string {
	left := " " + icon + " " + name
	pad := w - width(left) - width(info) - 1
	if pad < 1 {
		return bold + blue + truncate(left, w) + reset
	}
	return bold + blue + left + reset + strings.Repeat(" ", pad) + dim + info + reset
}

// width is the display width of s, ignoring escape sequences. Everything
// the built-ins print is single-width.
func width(s string) int {
	n := 0
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			j := i + 1
			switch {
			case j < len(s) && s[j] == '[': // CSI ... final
				j++
				for j < len(s) && (s[j] < 0x40 || s[j] > 0x7e) {
					j++
				}
			case j < len(s) && s[j] == ']': // OSC ... BEL or ESC \
				for j < len(s) && s[j] != 0x07 && !(s[j] == 0x1b && j+1 < len(s) && s[j+1] == '\\') {
					j++
				}
				if j < len(s) && s[j] == 0x1b {
					j++
				}
			}
			i = j + 1
			continue
		}
		_, sz := utf8.DecodeRuneInString(s[i:])
		i += sz
		n++
	}
	return n
}

// truncate cuts plain text to w cells, with an ellipsis.
func truncate(s string, w int) string {
	if w <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= w {
		return s
	}
	if w == 1 {
		return "…"
	}
	return string(r[:w-1]) + "…"
}

// center pads s (which may contain escapes) to the middle of w cells.
func center(s string, w int) string {
	pad := (w - width(s)) / 2
	if pad <= 0 {
		return s
	}
	return strings.Repeat(" ", pad) + s
}

// wrap word-wraps plain text to w cells, at most max lines.
func wrap(s string, w, max int) []string {
	var lines []string
	var cur string
	for _, word := range strings.Fields(s) {
		switch {
		case cur == "":
			cur = word
		case utf8.RuneCountInString(cur)+1+utf8.RuneCountInString(word) <= w:
			cur += " " + word
		default:
			lines = append(lines, truncate(cur, w))
			cur = word
		}
		if len(lines) == max {
			break
		}
	}
	if cur != "" && len(lines) < max {
		lines = append(lines, truncate(cur, w))
	}
	if len(lines) == max && max > 0 && len(strings.Fields(s)) > 0 {
		// Mark that text was cut if we stopped early.
		joined := strings.Join(lines, " ")
		if len(strings.Fields(joined)) < len(strings.Fields(s)) {
			last := []rune(lines[max-1])
			if len(last) >= w {
				last = last[:w-1]
			}
			lines[max-1] = string(last) + "…"
		}
	}
	return lines
}

var client = &http.Client{Timeout: 20 * time.Second}

// get fetches a URL. Some APIs refuse requests without a User-Agent, and
// some behind Cloudflare refuse browser-like ones, so germ names itself.
func get(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "germ-widget/1.0 (+https://github.com/zach-monroe/germ)")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("%s: %s", url, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 8<<20))
}

// errLine formats an update error for the bottom of a widget.
func errLine(err error, w int) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	if i := strings.Index(msg, ": "); i > 0 && strings.HasPrefix(msg, "Get ") {
		msg = msg[i+2:]
	}
	return red + truncate("⚠ "+msg, w) + reset
}

// fill pads lines to h rows and puts footer on the last one.
func fill(lines []string, h int, footer string) []string {
	if footer == "" {
		return lines
	}
	for len(lines) < h-1 {
		lines = append(lines, "")
	}
	if len(lines) >= h {
		lines = lines[:h-1]
	}
	return append(lines, footer)
}

// link makes text a clickable hyperlink (OSC 8); germ opens it on click.
func link(url, text string) string {
	if url == "" {
		return text
	}
	return "\x1b]8;;" + url + "\x1b\\" + text + "\x1b]8;;\x1b\\"
}

package app

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zach-monroe/germ/internal/config"
	"github.com/zach-monroe/germ/internal/pty"
	"github.com/zach-monroe/germ/internal/widgets"
)

// The home tab is a germ-native first tab showing a grid of widgets. Each
// widget is a program running in its own PTY-backed pane (see package
// widgets for the protocol), so any command that draws text can be a
// widget, and built-in widgets are just `germ widget NAME`.

// buildHome creates the home tab from the config, or returns nil.
func (a *App) buildHome() *tab {
	h := a.cfg.Home
	if !h.Enabled || len(h.Layout) == 0 {
		return nil
	}
	t := &tab{panes: map[int]*pane{}, home: true, name: "home", index: "⌂"}
	var rows []*node
	var rowWeights []float64
	for i, row := range h.Layout {
		var cells []*node
		var weights []float64
		for _, spec := range row {
			name, weight := parseWidgetSpec(spec)
			p := a.newWidgetPane(name)
			t.panes[p.id] = p
			if t.active == nil {
				t.active = p
			}
			cells = append(cells, leaf(p.id))
			weights = append(weights, weight)
		}
		if len(cells) == 0 {
			continue
		}
		rows = append(rows, chain(cells, weights, splitH))
		w := 1.0
		if i < len(h.RowHeights) && h.RowHeights[i] > 0 {
			w = h.RowHeights[i]
		}
		rowWeights = append(rowWeights, w)
	}
	if len(rows) == 0 {
		return nil
	}
	t.root = chain(rows, rowWeights, splitV)
	return t
}

// parseWidgetSpec splits "news:2" into a name and relative size.
func parseWidgetSpec(s string) (string, float64) {
	name, w, ok := strings.Cut(strings.TrimSpace(s), ":")
	if !ok {
		return name, 1
	}
	f, err := strconv.ParseFloat(w, 64)
	if err != nil || f <= 0 {
		return name, 1
	}
	return name, f
}

// chain joins nodes into a right-leaning split tree whose ratios give each
// node its weighted share.
func chain(nodes []*node, weights []float64, dir splitDir) *node {
	if len(nodes) == 1 {
		return nodes[0]
	}
	total := 0.0
	for _, w := range weights {
		total += w
	}
	rest := chain(nodes[1:], weights[1:], dir)
	s := &node{dir: dir, a: nodes[0], b: rest, ratio: weights[0] / total}
	nodes[0].parent, rest.parent = s, s
	return s
}

// homeCommand finds what to run for a widget.
func (a *App) homeCommand(w config.Widget) ([]string, error) {
	if len(w.Command) > 0 {
		cmd := append([]string(nil), w.Command...)
		cmd[0] = expandHome(cmd[0])
		return cmd, nil
	}
	user := filepath.Join(config.Dir(), "widgets", w.Name)
	if st, err := os.Stat(user); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
		return []string{user}, nil
	}
	if widgets.IsBuiltin(w.Name) {
		self, err := os.Executable()
		if err != nil {
			return nil, err
		}
		return []string{self, "widget", w.Name}, nil
	}
	return nil, fmt.Errorf("no widget %q: not built in (%s), no command in [widgets.%s], and no executable %s",
		w.Name, strings.Join(widgets.Names(), ", "), w.Name, user)
}

func expandHome(p string) string {
	if strings.HasPrefix(p, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, p[2:])
		}
	}
	return p
}

// widgetBackend runs a widget program and ignores keyboard input.
type widgetBackend struct {
	a     *App
	p     *pane
	argv  []string
	env   []string
	every time.Duration

	mu         sync.Mutex
	sess       *pty.Session
	cols, rows int
	stopped    bool
	poke       chan struct{} // rerun now (interval widgets, on resize)
}

func (b *widgetBackend) Write([]byte) {}
func (b *widgetBackend) Cwd() string  { return "" }
func (b *widgetBackend) Pid() int     { return 0 }

func (b *widgetBackend) Resize(cols, rows int) {
	b.mu.Lock()
	b.cols, b.rows = cols, rows
	if b.sess != nil {
		_ = b.sess.Resize(uint16(cols), uint16(rows))
	}
	b.mu.Unlock()
	select {
	case b.poke <- struct{}{}:
	default:
	}
}

func (b *widgetBackend) Close() {
	b.mu.Lock()
	b.stopped = true
	if b.sess != nil {
		b.sess.Close()
	}
	b.mu.Unlock()
	select {
	case b.poke <- struct{}{}:
	default:
	}
}

func (b *widgetBackend) isStopped() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.stopped
}

// newWidgetPane creates a pane for the named widget and starts it.
func (a *App) newWidgetPane(name string) *pane {
	p := a.addPane()
	p.term = a.newTermNoReplies(p, 1, 1)
	p.title.Store(&name)
	b := &widgetBackend{a: a, p: p, cols: 1, rows: 1, poke: make(chan struct{}, 1)}
	p.be = b
	w, err := a.cfg.Widget(name)
	if err == nil {
		b.argv, err = a.homeCommand(w)
		b.env = append(append([]string(nil), a.baseEnv...), w.Env...)
		b.env = append(b.env, "GERM_WIDGET="+name)
		b.every = w.Interval
	}
	if err != nil {
		p.term.Write([]byte("\x1b[?25l\x1b[31m" + strings.ReplaceAll(err.Error(), "\n", "\r\n") + "\x1b[0m"))
		return p
	}
	go b.run()
	return p
}

// run supervises the widget: long-running widgets are restarted if they
// exit; interval widgets are rerun and their whole output shown at once.
func (b *widgetBackend) run() {
	// Wait for the first layout so the widget starts at its real size.
	select {
	case <-b.poke:
	case <-time.After(2 * time.Second):
	}
	backoff := time.Second
	for !b.isStopped() {
		start := time.Now()
		out, err := b.runOnce()
		if b.isStopped() {
			return
		}
		if b.every > 0 {
			if err != nil && len(out) == 0 {
				out = []byte(fmt.Sprintf("\x1b[31m%s: %v\x1b[0m", b.argv[0], err))
			}
			b.show(append([]byte("\x1b[?25l\x1b[H\x1b[2J"), out...))
			b.wait(b.every)
			continue
		}
		// A long-running widget exited: say so, then restart it.
		if time.Since(start) > time.Minute {
			backoff = time.Second
		}
		msg := "exited"
		if err != nil {
			msg = err.Error()
		}
		b.show([]byte(fmt.Sprintf("\r\n\x1b[31mwidget %s; restarting in %s\x1b[0m", msg, backoff)))
		b.wait(backoff)
		backoff = min(backoff*2, time.Minute)
	}
}

// wait sleeps for d or until poked (resize or close).
func (b *widgetBackend) wait(d time.Duration) {
	select {
	case <-b.poke:
	case <-time.After(d):
	}
}

func (b *widgetBackend) show(data []byte) {
	b.p.term.Write(data)
	b.a.wake()
}

// runOnce runs the widget program in a PTY. Long-running widgets stream
// straight into the pane; interval widgets' output is collected.
func (b *widgetBackend) runOnce() ([]byte, error) {
	b.mu.Lock()
	cols, rows := b.cols, b.rows
	b.mu.Unlock()
	sess, err := pty.StartIn("", b.argv, uint16(max(cols, 1)), uint16(max(rows, 1)), b.env...)
	if err != nil {
		return nil, err
	}
	b.mu.Lock()
	b.sess = sess
	stopped := b.stopped
	b.mu.Unlock()
	if stopped {
		sess.Close()
	}
	var collected bytes.Buffer
	buf := make([]byte, 32*1024)
	for {
		n, rerr := sess.Read(buf)
		if n > 0 {
			if b.every > 0 {
				if collected.Len() < 1<<20 {
					collected.Write(buf[:n])
				}
			} else {
				b.show(buf[:n])
			}
		}
		if rerr != nil {
			break
		}
	}
	err = sess.Wait()
	sess.Close()
	b.mu.Lock()
	b.sess = nil
	b.mu.Unlock()
	if _, ok := err.(*exec.ExitError); ok && b.every > 0 {
		err = fmt.Errorf("%s: %v", filepath.Base(b.argv[0]), err)
	}
	return collected.Bytes(), err
}

// homeConfigChanged reports whether a reload needs a new home tab.
func homeConfigChanged(old, cur *config.Config) bool {
	return !reflect.DeepEqual(old.Home, cur.Home) || !reflect.DeepEqual(old.Widgets, cur.Widgets)
}

// rebuildHome replaces the home tab after a config change.
func (a *App) rebuildHome() {
	wasHome := false
	if len(a.tabs) > 0 && a.tabs[0].home {
		old := a.tabs[0]
		wasHome = a.cur == 0
		for _, p := range old.panes {
			p.be.Close()
			delete(a.panes, p.id)
		}
		a.tabs = a.tabs[1:]
		if a.cur > 0 {
			a.cur--
		}
	}
	if h := a.buildHome(); h != nil {
		a.tabs = append([]*tab{h}, a.tabs...)
		if wasHome {
			a.cur = 0
		} else {
			a.cur++
		}
	}
	a.cur = min(a.cur, max(0, len(a.tabs)-1))
	a.relayout()
}

// workTabs counts tabs other than home: germ quits when none are left.
func (a *App) workTabs() int {
	n := 0
	for _, t := range a.tabs {
		if !t.home {
			n++
		}
	}
	return n
}

// homeFocused reports whether the home tab should be shown at startup.
func (a *App) homeFocused() bool {
	return len(a.tabs) > 0 && a.tabs[0].home && a.cfg.Home.Focus
}

// toggleHome shows the home tab, or from the home tab returns to the tab
// you came from.
func (a *App) toggleHome() bool {
	if len(a.tabs) == 0 || !a.tabs[0].home {
		return false
	}
	if a.cur != 0 {
		a.switchTab(0)
		return true
	}
	for i, t := range a.tabs {
		if t == a.prevTab && i != 0 {
			a.switchTab(i)
			return true
		}
	}
	if len(a.tabs) > 1 {
		a.switchTab(1)
	}
	return true
}

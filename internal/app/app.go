// Package app owns the window and the main loop, and wires panes (each a
// terminal plus a backend) into tabs.
//
// Each pane's reader goroutine feeds output into its term.Terminal and
// wakes the main loop; the main loop (on the locked OS thread) handles
// window events and redraws when something changed.
package app

import (
	"fmt"
	"image"
	"image/png"
	"io"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/go-gl/gl/v3.3-core/gl"
	"github.com/go-gl/glfw/v3.3/glfw"

	"github.com/zach-monroe/germ/internal/config"
	"github.com/zach-monroe/germ/internal/font"
	"github.com/zach-monroe/germ/internal/render"
	"github.com/zach-monroe/germ/internal/shellint"
	"github.com/zach-monroe/germ/internal/terminfo"
	"github.com/zach-monroe/germ/internal/theme"
)

// Options configures a run.
type Options struct {
	Command      []string // first pane's argv, e.g. ["/usr/bin/fish"]
	ShellCommand []string // argv for new tabs and splits (default: Command)
	Out          io.Writer
	Quote        bool // log child output as Go-quoted strings
	Dump         bool // print the active pane's screen text at exit

	Config     config.Config
	ConfigPath string
	Overrides  Overrides // command-line flags that beat the config file

	// UseTmux attaches to tmux in control mode instead of running Command.
	// TmuxSession names the session; empty picks a free germ-N session.
	UseTmux     bool
	TmuxSession string

	// Screenshot, if set, saves the window contents as PNG to this path
	// after ScreenshotDelay and exits. Used for testing the renderer.
	Screenshot      string
	ScreenshotDelay time.Duration
}

// Overrides are settings given on the command line; they take precedence
// over the config file, including after a reload. Zero means "not set".
type Overrides struct {
	FontFamily string
	FontSize   float64
	Theme      string
}

func (o Overrides) apply(c *config.Config) {
	if o.FontFamily != "" {
		c.Font.Family = o.FontFamily
	}
	if o.FontSize > 0 {
		c.Font.Size = o.FontSize
	}
	if o.Theme != "" {
		c.Theme = o.Theme
	}
}

type App struct {
	opts   Options
	win    *glfw.Window
	render *render.Renderer

	// Cross-goroutine signals.
	wakePending atomic.Bool
	paneExited  atomic.Bool
	reload      atomic.Bool
	focusedA    atomic.Bool
	attention   atomic.Bool
	agentPoll   atomic.Bool // time to re-detect agents
	ctl         *ctlServer
	prevTab     *tab // the tab shown before the last switch (for toggle_home)
	animating   bool // a working agent's spinner is showing
	clipboard   atomic.Pointer[string]

	// Main-thread state.
	tabs       []*tab
	cur        int
	panes      map[int]*pane
	nextID     int
	tabSurf    *render.Surface
	tabSpans   []tabSpan
	tmux       *tmuxController
	lastExited *pane

	gridCols, gridRows int
	focused            bool
	forceRedraw        bool
	scale              float32 // content scale (DPI / 96)
	fontSize           float64 // current size, which zoom changes
	cfg                config.Config
	bindings           map[config.Chord]config.Action
	baseEnv            []string // TERM and friends for new panes
	lastTitle          string
	quitting           bool

	mouse        mouseState
	suppressChar bool // the current key press was already encoded
}

// Run opens the window and blocks until it is closed or the last pane exits.
// It must be called from the main OS thread (see runtime.LockOSThread in main).
func Run(opts Options) error {
	cfg := opts.Config
	opts.Overrides.apply(&cfg)
	if _, err := cfg.ResolveTheme(); err != nil {
		fmt.Fprintln(os.Stderr, "germ:", err, "(using default theme)")
		cfg.Theme = theme.DefaultName
	}
	bindings, _ := cfg.Bindings() // validated by config.Parse

	if err := glfw.Init(); err != nil {
		return fmt.Errorf("glfw init: %w", err)
	}
	defer glfw.Terminate()

	glfw.WindowHint(glfw.ContextVersionMajor, 3)
	glfw.WindowHint(glfw.ContextVersionMinor, 3)
	glfw.WindowHint(glfw.OpenGLProfile, glfw.OpenGLCoreProfile)
	glfw.WindowHint(glfw.OpenGLForwardCompatible, glfw.True)

	// Start hidden so the window can be sized to the font before showing.
	glfw.WindowHint(glfw.Visible, glfw.False)
	win, err := glfw.CreateWindow(800, 600, "germ", nil, nil)
	if err != nil {
		return fmt.Errorf("create window: %w", err)
	}
	defer win.Destroy()
	win.MakeContextCurrent()
	glfw.SwapInterval(1)
	if err := gl.Init(); err != nil {
		return fmt.Errorf("gl init: %w", err)
	}

	scale, _ := win.GetContentScale()
	if scale <= 0 {
		scale = 1
	}
	fonts, err := font.Load(cfg.Font.Family, cfg.Font.Size, 96*float64(scale))
	if err != nil {
		return err
	}
	rnd, err := render.New(fonts, scaledPad(cfg, scale))
	if err != nil {
		return err
	}

	// Size the window for the configured grid. Window coordinates may
	// differ from framebuffer pixels on scaled displays.
	cw, ch := rnd.CellSize()
	ww, _ := win.GetSize()
	fbw, _ := win.GetFramebufferSize()
	ratio := float64(fbw) / float64(max(ww, 1))
	win.SetSize(int(float64(cfg.Window.Columns*cw+2*rnd.Pad())/ratio), int(float64(cfg.Window.Rows*ch+2*rnd.Pad())/ratio))
	win.Show()
	fbw, fbh := settleSize(win)

	a := &App{
		opts: opts, win: win, render: rnd, panes: map[int]*pane{},
		focused: true, forceRedraw: true, scale: scale, fontSize: cfg.Font.Size,
		cfg: cfg, bindings: bindings,
	}
	a.focusedA.Store(true)
	a.tabSurf = rnd.NewSurface()
	a.gridCols, a.gridRows = rnd.Resize(fbw, fbh)

	switch cfg.Shell.Term {
	case "":
		a.baseEnv = terminfo.Env() // TERM=xterm-germ if the entry installs
	default:
		a.baseEnv = []string{"TERM=" + cfg.Shell.Term}
	}

	if opts.ConfigPath != "" {
		a.baseEnv = append(a.baseEnv, "GERM_CONFIG="+opts.ConfigPath)
	}
	a.startControl()
	defer a.stopControl()
	if h := a.buildHome(); h != nil {
		a.tabs = []*tab{h}
	}
	tmuxStarted := false
	if opts.UseTmux {
		if err := a.startTmux(opts.TmuxSession); err != nil {
			fmt.Fprintln(os.Stderr, "germ:", err, "(starting a plain shell instead)")
		} else {
			tmuxStarted = true
		}
	}
	if !tmuxStarted {
		p, err := a.newLocalPane(opts.Command, "", a.gridCols, a.gridRows)
		if err != nil {
			return err
		}
		a.tabs = append(a.tabs, newTab(p))
		if !a.homeFocused() {
			a.cur = len(a.tabs) - 1
		}
	}
	a.relayout()

	win.SetCharModsCallback(a.onChar)
	win.SetKeyCallback(a.onKey)
	win.SetMouseButtonCallback(a.onMouseButton)
	win.SetCursorPosCallback(a.onCursorPos)
	win.SetScrollCallback(a.onScroll)
	win.SetFramebufferSizeCallback(a.onResize)
	win.SetRefreshCallback(func(*glfw.Window) { a.forceRedraw = true; a.draw() })
	win.SetFocusCallback(a.onFocus)
	win.SetDropCallback(a.onDrop)

	a.startAgentTicker()
	if stop, err := config.Watch(func() { a.reload.Store(true); glfw.PostEmptyEvent() }); err == nil {
		defer stop()
	}

	a.draw()
	var shotAt time.Time
	if opts.Screenshot != "" {
		shotAt = time.Now().Add(opts.ScreenshotDelay)
	}
	for !win.ShouldClose() && !a.quitting {
		if !shotAt.IsZero() {
			glfw.WaitEventsTimeout(0.05)
			if time.Now().After(shotAt) {
				return a.screenshot(opts.Screenshot)
			}
		} else if a.animating {
			glfw.WaitEventsTimeout(0.1) // spinner frames
		} else {
			glfw.WaitEvents() // blocks: zero CPU while idle
		}
		if a.tmux != nil {
			a.tmux.process()
		}
		a.processControl()
		if a.paneExited.Swap(false) && !a.reapPanes() {
			break
		}
		if a.workTabs() == 0 && (a.tmux == nil || a.tmux.ready) {
			break
		}
		if text := a.clipboard.Swap(nil); text != nil {
			glfw.SetClipboardString(*text)
		}
		if a.attention.Swap(false) {
			win.RequestAttention()
		}
		if a.reload.Swap(false) {
			a.reloadConfig()
		}
		if anim := a.agentTick(a.agentPoll.Swap(false)); anim || anim != a.animating {
			a.animating = anim
			a.forceRedraw = true // tab badges
		}
		a.draw()
	}
	if a.tmux != nil {
		a.tmux.detach()
	}
	if opts.Dump && opts.Out != nil {
		if p := a.lastActive(); p != nil {
			fmt.Fprintln(opts.Out, strings.TrimRight(p.term.Text(), "\n"))
		}
	}
	for _, p := range a.panes {
		p.be.Close()
	}
	return nil
}

// lastActive returns the active pane, or the last one to exit.
func (a *App) lastActive() *pane {
	if p := a.active(); p != nil {
		return p
	}
	return a.lastExited
}

func shellintEnv(argv []string) []string { return shellint.Env(argv) }

// settleSize waits briefly for the window manager to finish placing the
// window. Tiling WMs (i3, sway) resize a window right after it maps; if the
// shell started at the requested size, its first output (prompt, fastfetch)
// would be laid out for the wrong width and then mangled by reflow.
func settleSize(win *glfw.Window) (int, int) {
	w, h := win.GetFramebufferSize()
	deadline := time.Now().Add(300 * time.Millisecond)
	stableSince := time.Now()
	for time.Now().Before(deadline) {
		glfw.WaitEventsTimeout(0.01)
		nw, nh := win.GetFramebufferSize()
		if nw != w || nh != h {
			w, h, stableSince = nw, nh, time.Now()
		} else if time.Since(stableSince) > 60*time.Millisecond {
			break
		}
	}
	return w, h
}

// screenshot redraws into the back buffer and saves it as a PNG.
func (a *App) screenshot(path string) error {
	a.forceRedraw = true
	a.drawFrame()
	w, h := a.win.GetFramebufferSize()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	gl.ReadBuffer(gl.BACK)
	gl.ReadPixels(0, 0, int32(w), int32(h), gl.RGBA, gl.UNSIGNED_BYTE, gl.Ptr(img.Pix))
	// GL rows are bottom-up.
	row := make([]byte, img.Stride)
	for y := 0; y < h/2; y++ {
		top, bot := img.Pix[y*img.Stride:(y+1)*img.Stride], img.Pix[(h-1-y)*img.Stride:(h-y)*img.Stride]
		copy(row, top)
		copy(top, bot)
		copy(bot, row)
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, img)
}

// onClipboard handles OSC 52 writes (reader goroutine, terminal locked).
// GLFW's clipboard must be used from the main thread, so hand it over.
func (a *App) onClipboard(text string, clipboard, primary bool) {
	if clipboard {
		a.clipboard.Store(&text)
		glfw.PostEmptyEvent()
	}
	if primary {
		setPrimary(text)
	}
}

func (a *App) log(p []byte) {
	if a.opts.Quote && a.opts.Out != nil {
		fmt.Fprintln(a.opts.Out, strconv.Quote(string(p)))
	}
}

// sendUser sends input typed or pasted by the user to the active pane and
// snaps its view back to the live screen.
func (a *App) sendUser(b []byte) {
	p := a.active()
	if p == nil || len(b) == 0 {
		return
	}
	if p.offset != 0 {
		p.offset = 0
		a.forceRedraw = true
	}
	p.agent.lastInput.Store(time.Now().UnixNano())
	p.send(b)
}

func (a *App) view(p *pane, t *tab) render.View {
	return render.View{
		Offset:    p.offset,
		Focused:   a.focused && p == t.active,
		Selection: p.sel.render(p.term),
		Link:      p.link,
		Force:     a.forceRedraw,
	}
}

func (a *App) draw() {
	a.wakePending.Store(false)
	if a.drawFrame() {
		a.win.SwapBuffers()
	}
	a.forceRedraw = false
	if p := a.active(); p != nil {
		if title := p.titleString(); title != a.lastTitle {
			a.lastTitle = title
			a.win.SetTitle(title)
		}
	}
}

// drawFrame updates every visible surface and draws a frame if anything
// changed. It reports whether a frame was drawn.
func (a *App) drawFrame() bool {
	t := a.tab()
	if t == nil || t.active == nil {
		return false
	}
	panes := t.visible()
	changed := a.forceRedraw
	surfaces := make([]*render.Surface, 0, len(panes)+1)
	for _, p := range panes {
		// Keep a scrolled-back view anchored on the same text as new
		// lines arrive.
		p.term.Lock()
		pushed, sbLen := p.term.Pushed(), p.term.ScrollbackLen()
		p.term.Unlock()
		if p.offset > 0 {
			p.offset = min(p.offset+int(pushed-p.lastPushed), sbLen)
		}
		p.lastPushed = pushed

		ox, oy, gutter := a.paneOrigin(p)
		if p.surf.Update(p.term, a.view(p, t), ox, oy, gutter) {
			changed = true
		}
		surfaces = append(surfaces, p.surf)
	}
	if !changed {
		return false
	}
	th := t.active.surf.Theme()
	var rects []render.Rect
	if a.barRows() > 0 {
		line, spans := a.tabBarLine(th)
		a.tabSpans = spans
		a.tabSurf.UpdateLine(line, th, a.render.Pad(), a.render.Pad())
		surfaces = append(surfaces, a.tabSurf)
	}
	rects = append(rects, a.separatorRects(t, blendRGB(th.Background, th.Foreground, 0.25))...)
	a.render.Frame(t.active.surf.DefaultBG(), surfaces, rects)
	return true
}

func (a *App) onResize(_ *glfw.Window, w, h int) {
	a.gridCols, a.gridRows = a.render.Resize(w, h)
	if a.tmux != nil {
		a.tmux.clientSize(a.content())
	}
	a.relayout()
}

// refit recomputes the grid after the cell size or padding changed.
func (a *App) refit() {
	w, h := a.win.GetFramebufferSize()
	a.onResize(a.win, w, h)
}

func (a *App) onFocus(_ *glfw.Window, focused bool) {
	a.focused = focused
	a.focusedA.Store(focused)
	a.forceRedraw = true
	a.focusChanged(a.active(), focused)
}

func scaledPad(cfg config.Config, scale float32) int {
	return int(float32(cfg.Window.Padding)*scale + 0.5)
}

// reloadConfig re-reads the config file and applies what can change live:
// theme and colors, font, padding, and key bindings. Errors keep the
// previous config.
func (a *App) reloadConfig() {
	cfg, warnings, err := config.Load(a.opts.ConfigPath)
	for _, w := range warnings {
		fmt.Fprintf(os.Stderr, "germ: %s: %s\n", a.opts.ConfigPath, w)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "germ: %s: %v (keeping previous config)\n", a.opts.ConfigPath, err)
		return
	}
	a.opts.Overrides.apply(&cfg)
	th, err := cfg.ResolveTheme()
	if err != nil {
		fmt.Fprintln(os.Stderr, "germ:", err, "(keeping previous config)")
		return
	}
	old := a.cfg
	a.cfg = cfg
	a.bindings, _ = cfg.Bindings()
	for _, p := range a.panes {
		p.term.SetTheme(th)
	}

	refit := false
	if cfg.Font != old.Font {
		a.fontSize = cfg.Font.Size
		if fonts, err := font.Load(cfg.Font.Family, cfg.Font.Size, 96*float64(a.scale)); err == nil {
			a.render.SetFonts(fonts)
			refit = true
		} else {
			fmt.Fprintln(os.Stderr, "germ:", err)
		}
	}
	if cfg.Window.Padding != old.Window.Padding {
		a.render.SetPad(scaledPad(cfg, a.scale))
		refit = true
	}
	if refit {
		a.refit()
	}
	if homeConfigChanged(&old, &a.cfg) {
		a.rebuildHome()
	}
	a.forceRedraw = true
}

// setFontSize reloads fonts at a new size and refits the grid.
func (a *App) setFontSize(size float64) {
	size = max(4, min(size, 96))
	fonts, err := font.Load(a.cfg.Font.Family, size, 96*float64(a.scale))
	if err != nil {
		fmt.Fprintln(os.Stderr, "germ:", err)
		return
	}
	a.fontSize = size
	a.render.SetFonts(fonts)
	a.refit()
}

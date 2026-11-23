// Package app owns the window, the main loop, and wires the PTY to input/output.
//
// The PTY reader goroutine feeds child output into a term.Terminal and wakes
// the main loop; the main loop (on the locked OS thread) handles window
// events and redraws with the renderer when something changed.
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
	"github.com/zach-monroe/germ/internal/pty"
	"github.com/zach-monroe/germ/internal/render"
	"github.com/zach-monroe/germ/internal/term"
	"github.com/zach-monroe/germ/internal/theme"
)

// Options configures a run.
type Options struct {
	Command []string  // child argv, e.g. ["/usr/bin/fish"]
	Out     io.Writer // debug output destination
	Quote   bool      // log child output as Go-quoted strings (shows escape sequences)
	Dump    bool      // print the screen text when the session ends

	Config     config.Config
	ConfigPath string
	Overrides  Overrides // command-line flags that beat the config file

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
	sess   *pty.Session
	term   *term.Terminal
	render *render.Renderer
	input  chan []byte // bytes queued for the child (keys and query replies)
	title  atomic.Pointer[string]
	exited atomic.Bool
	// wakePending is set when the reader has posted a wakeup the main loop
	// hasn't drawn for yet.
	wakePending atomic.Bool

	// Main-thread state below.
	focused     bool
	forceRedraw bool
	scale       float32 // content scale (DPI / 96)
	fontSize    float64 // current size, which zoom changes
	cfg         config.Config
	bindings    map[config.Chord]config.Action
	reload      atomic.Bool // set by the config watcher

	offset     int   // lines scrolled back into history
	lastPushed int64 // term.Pushed() at the last draw, to keep the view anchored
	scrollAcc  float64

	sel     selection
	mouse   mouseState
	link    render.Link
	linkURL string

	suppressChar bool // the current key press was already encoded
}

// Run opens the window and blocks until it is closed or the child exits.
// It must be called from the main OS thread (see runtime.LockOSThread in main).
func Run(opts Options) error {
	cfg := opts.Config
	opts.Overrides.apply(&cfg)
	th, err := cfg.ResolveTheme()
	if err != nil {
		fmt.Fprintln(os.Stderr, "germ:", err, "(using default theme)")
		th = theme.Default()
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
	fbw, fbh := win.GetFramebufferSize()
	ratio := float64(fbw) / float64(max(ww, 1))
	win.SetSize(int(float64(cfg.Window.Columns*cw+2*rnd.Pad())/ratio), int(float64(cfg.Window.Rows*ch+2*rnd.Pad())/ratio))
	win.Show()
	fbw, fbh = settleSize(win)
	cols, rows := rnd.Resize(fbw, fbh)

	sess, err := pty.Start(opts.Command, uint16(cols), uint16(rows))
	if err != nil {
		return err
	}
	defer sess.Close()

	a := &App{
		opts: opts, win: win, sess: sess, render: rnd,
		input: make(chan []byte, 256), focused: true, forceRedraw: true,
		scale: scale, fontSize: cfg.Font.Size, cfg: cfg, bindings: bindings,
	}
	a.term = term.New(cols, rows, term.Options{
		Scrollback: cfg.Scrollback.Lines,
		Theme:      th,
		Responder:  chanWriter(a.input),
		OnTitle:    func(s string) { glfw.PostEmptyEvent(); a.title.Store(&s) },
	})
	go a.writeLoop()
	win.SetCharModsCallback(a.onChar)
	win.SetKeyCallback(a.onKey)
	win.SetMouseButtonCallback(a.onMouseButton)
	win.SetCursorPosCallback(a.onCursorPos)
	win.SetScrollCallback(a.onScroll)
	win.SetFramebufferSizeCallback(a.onResize)
	win.SetRefreshCallback(func(*glfw.Window) { a.forceRedraw = true; a.draw() })
	win.SetFocusCallback(a.onFocus)

	go a.readLoop()
	if stop, err := config.Watch(func() { a.reload.Store(true); glfw.PostEmptyEvent() }); err == nil {
		defer stop()
	}

	a.draw()
	var shotAt time.Time
	if opts.Screenshot != "" {
		shotAt = time.Now().Add(opts.ScreenshotDelay)
	}
	for !win.ShouldClose() && !a.exited.Load() {
		if !shotAt.IsZero() {
			glfw.WaitEventsTimeout(0.05)
			if time.Now().After(shotAt) {
				return a.screenshot(opts.Screenshot)
			}
		} else {
			glfw.WaitEvents() // blocks: zero CPU while idle
		}
		if t := a.title.Swap(nil); t != nil {
			win.SetTitle(*t)
		}
		if a.reload.Swap(false) {
			a.reloadConfig()
		}
		a.draw()
	}
	if opts.Dump && opts.Out != nil {
		fmt.Fprintln(opts.Out, strings.TrimRight(a.term.Text(), "\n"))
	}
	return nil
}

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
	a.render.Draw(a.term, a.view())
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

// chanWriter queues writes on a channel so the reader goroutine never
// blocks on the PTY while emitting query replies.
type chanWriter chan []byte

func (c chanWriter) Write(p []byte) (int, error) {
	c <- append([]byte(nil), p...)
	return len(p), nil
}

// writeLoop is the only goroutine that writes to the PTY, preserving the
// order of keystrokes and replies without blocking the UI thread.
func (a *App) writeLoop() {
	for p := range a.input {
		if _, err := a.sess.Write(p); err != nil {
			return
		}
	}
}

// readLoop drains child output on its own goroutine.
func (a *App) readLoop() {
	buf := make([]byte, 64*1024)
	for {
		n, err := a.sess.Read(buf)
		if n > 0 {
			a.log(buf[:n])
			a.term.Write(buf[:n])
			// Wake the main loop to redraw, at most once per frame: the
			// X11 wakeup is a cgo call plus a round-trip to the server.
			if !a.wakePending.Swap(true) {
				glfw.PostEmptyEvent()
			}
		}
		if err != nil {
			// Any error ends the session: on Linux, EIO is the normal
			// "child exited" signal from a PTY master.
			a.exited.Store(true)
			glfw.PostEmptyEvent() // wake the main loop so it can exit
			return
		}
	}
}

func (a *App) log(p []byte) {
	if a.opts.Quote && a.opts.Out != nil {
		fmt.Fprintln(a.opts.Out, strconv.Quote(string(p)))
	}
}

// send queues bytes for the child. Safe from any goroutine.
func (a *App) send(p []byte) {
	if len(p) == 0 {
		return
	}
	select {
	case a.input <- p:
	default:
		fmt.Fprintln(os.Stderr, "germ: input queue full, dropping input")
	}
}

// sendUser sends input typed or pasted by the user: it also snaps the
// view back to the live screen.
func (a *App) sendUser(p []byte) {
	if len(p) == 0 {
		return
	}
	if a.offset != 0 {
		a.offset = 0
		a.forceRedraw = true
	}
	a.send(p)
}

func (a *App) view() render.View {
	return render.View{
		Offset:    a.offset,
		Focused:   a.focused,
		Selection: a.sel.render(a.term),
		Link:      a.link,
		Force:     a.forceRedraw,
	}
}

func (a *App) draw() {
	a.wakePending.Store(false)

	// Keep a scrolled-back view anchored on the same text as new lines
	// arrive, and drop a selection on the alternate screen when it closes.
	a.term.Lock()
	pushed, sbLen := a.term.Pushed(), a.term.ScrollbackLen()
	a.term.Unlock()
	if a.offset > 0 {
		a.offset = min(a.offset+int(pushed-a.lastPushed), sbLen)
	}
	a.lastPushed = pushed

	if a.render.Draw(a.term, a.view()) {
		a.win.SwapBuffers()
	}
	a.forceRedraw = false
}

func (a *App) onResize(_ *glfw.Window, w, h int) {
	a.resizeGrid(w, h)
}

func (a *App) resizeGrid(w, h int) {
	cols, rows := a.render.Resize(w, h)
	a.term.Resize(cols, rows)
	a.sel.clear() // absolute rows are renumbered by reflow
	a.offset = 0
	a.term.Lock()
	a.lastPushed = a.term.Pushed()
	a.term.Unlock()
	a.forceRedraw = true
	if err := a.sess.Resize(uint16(cols), uint16(rows)); err != nil {
		fmt.Fprintln(os.Stderr, "germ: resize:", err)
	}
}

func (a *App) onFocus(_ *glfw.Window, focused bool) {
	a.focused = focused
	a.forceRedraw = true
	a.term.Lock()
	report := a.term.Modes().FocusEvents
	a.term.Unlock()
	if report {
		a.send(inputFocus(focused))
	}
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
	a.term.SetTheme(th)

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
		a.resizeGrid(a.win.GetFramebufferSize())
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
	a.resizeGrid(a.win.GetFramebufferSize())
}

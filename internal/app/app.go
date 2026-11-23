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

	FontFamily string  // fontconfig family, e.g. "JetBrains Mono"
	FontSize   float64 // points

	// Screenshot, if set, saves the window contents as PNG to this path
	// after ScreenshotDelay and exits. Used for testing the renderer.
	Screenshot      string
	ScreenshotDelay time.Duration
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

	focused     bool
	forceRedraw bool
}

// Run opens the window and blocks until it is closed or the child exits.
// It must be called from the main OS thread (see runtime.LockOSThread in main).
func Run(opts Options) error {
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
	fonts, err := font.Load(opts.FontFamily, opts.FontSize, 96*float64(scale))
	if err != nil {
		return err
	}
	rnd, err := render.New(fonts, theme.Default(), int(4*scale+0.5))
	if err != nil {
		return err
	}

	// Size the window for 100x30 cells. Window coordinates may differ from
	// framebuffer pixels on scaled displays.
	cw, ch := rnd.CellSize()
	ww, _ := win.GetSize()
	fbw, fbh := win.GetFramebufferSize()
	ratio := float64(fbw) / float64(max(ww, 1))
	win.SetSize(int(float64(100*cw+2*rnd.Pad())/ratio), int(float64(30*ch+2*rnd.Pad())/ratio))
	win.Show()
	fbw, fbh = win.GetFramebufferSize()
	cols, rows := rnd.Resize(fbw, fbh)

	sess, err := pty.Start(opts.Command, uint16(cols), uint16(rows))
	if err != nil {
		return err
	}
	defer sess.Close()

	a := &App{opts: opts, win: win, sess: sess, render: rnd, input: make(chan []byte, 256), focused: true, forceRedraw: true}
	a.term = term.New(cols, rows, term.Options{
		Scrollback: 10000,
		Responder:  chanWriter(a.input),
		OnTitle:    func(s string) { glfw.PostEmptyEvent(); a.title.Store(&s) },
	})
	go a.writeLoop()
	win.SetCharModsCallback(a.onChar)
	win.SetKeyCallback(a.onKey)
	win.SetFramebufferSizeCallback(a.onResize)
	win.SetRefreshCallback(func(*glfw.Window) { a.forceRedraw = true; a.draw() })
	win.SetFocusCallback(func(_ *glfw.Window, f bool) { a.focused = f; a.forceRedraw = true })

	go a.readLoop()

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
		a.draw()
	}
	if opts.Dump && opts.Out != nil {
		fmt.Fprintln(opts.Out, strings.TrimRight(a.term.Text(), "\n"))
	}
	return nil
}

// screenshot redraws into the back buffer and saves it as a PNG.
func (a *App) screenshot(path string) error {
	a.render.Draw(a.term, a.focused, true)
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

func (a *App) send(p []byte) {
	select {
	case a.input <- p:
	default:
		fmt.Fprintln(os.Stderr, "germ: input queue full, dropping keystroke")
	}
}

func (a *App) draw() {
	a.wakePending.Store(false)
	if a.render.Draw(a.term, a.focused, a.forceRedraw) {
		a.win.SwapBuffers()
	}
	a.forceRedraw = false
}

func (a *App) onResize(_ *glfw.Window, w, h int) {
	cols, rows := a.render.Resize(w, h)
	a.term.Resize(cols, rows)
	a.forceRedraw = true
	if err := a.sess.Resize(uint16(cols), uint16(rows)); err != nil {
		fmt.Fprintln(os.Stderr, "germ: resize:", err)
	}
}

// onChar handles printable text (already composed by the OS/IME).
func (a *App) onChar(_ *glfw.Window, r rune, mods glfw.ModifierKey) {
	if mods&glfw.ModAlt != 0 {
		a.send([]byte{0x1b}) // Alt prefixes ESC (xterm "metaSendsEscape")
	}
	a.send([]byte(string(r)))
}

// onKey handles non-text keys and Ctrl combos. Minimal legacy xterm encoding;
// the full encoder (incl. kitty keyboard protocol) lands in Phase 3.
func (a *App) onKey(_ *glfw.Window, key glfw.Key, _ int, action glfw.Action, mods glfw.ModifierKey) {
	if action == glfw.Release {
		return
	}
	if mods&glfw.ModControl != 0 && key >= glfw.KeyA && key <= glfw.KeyZ {
		b := []byte{byte(key-glfw.KeyA) + 1}
		if mods&glfw.ModAlt != 0 {
			b = append([]byte{0x1b}, b...)
		}
		a.send(b)
		return
	}
	if seq, ok := keySeq[key]; ok {
		a.send([]byte(seq))
	}
}

var keySeq = map[glfw.Key]string{
	glfw.KeyEnter:     "\r",
	glfw.KeyKPEnter:   "\r",
	glfw.KeyTab:       "\t",
	glfw.KeyBackspace: "\x7f",
	glfw.KeyEscape:    "\x1b",
	glfw.KeyUp:        "\x1b[A",
	glfw.KeyDown:      "\x1b[B",
	glfw.KeyRight:     "\x1b[C",
	glfw.KeyLeft:      "\x1b[D",
	glfw.KeyHome:      "\x1b[H",
	glfw.KeyEnd:       "\x1b[F",
	glfw.KeyInsert:    "\x1b[2~",
	glfw.KeyDelete:    "\x1b[3~",
	glfw.KeyPageUp:    "\x1b[5~",
	glfw.KeyPageDown:  "\x1b[6~",
}

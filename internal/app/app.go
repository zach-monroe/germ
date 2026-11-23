// Package app owns the window, the main loop, and wires the PTY to input/output.
//
// Phase 1: child output drives a term.Terminal (so queries like DA1 get
// answered), but nothing is drawn yet: the window is a cleared GL surface
// that accepts keyboard input. Use -dump to print the final screen.
package app

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/go-gl/gl/v3.3-core/gl"
	"github.com/go-gl/glfw/v3.3/glfw"

	"github.com/zach-monroe/germ/internal/pty"
	"github.com/zach-monroe/germ/internal/term"
)

// Placeholder cell metrics until the font package exists (Phase 2).
const (
	cellW = 8
	cellH = 16
)

// Options configures a run.
type Options struct {
	Command []string  // child argv, e.g. ["/usr/bin/fish"]
	Out     io.Writer // debug output destination
	Quote   bool      // log child output as Go-quoted strings (shows escape sequences)
	Dump    bool      // print the screen text when the session ends
}

type App struct {
	opts   Options
	win    *glfw.Window
	sess   *pty.Session
	term   *term.Terminal
	input  chan []byte // bytes queued for the child (keys and query replies)
	title  atomic.Pointer[string]
	exited atomic.Bool
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

	win, err := glfw.CreateWindow(80*cellW, 24*cellH, "germ", nil, nil)
	if err != nil {
		return fmt.Errorf("create window: %w", err)
	}
	defer win.Destroy()
	win.MakeContextCurrent()
	glfw.SwapInterval(1)
	if err := gl.Init(); err != nil {
		return fmt.Errorf("gl init: %w", err)
	}

	fbw, fbh := win.GetFramebufferSize()
	cols, rows := gridSize(fbw, fbh)
	sess, err := pty.Start(opts.Command, cols, rows)
	if err != nil {
		return err
	}
	defer sess.Close()

	a := &App{opts: opts, win: win, sess: sess, input: make(chan []byte, 256)}
	a.term = term.New(int(cols), int(rows), term.Options{
		Scrollback: 10000,
		Responder:  chanWriter(a.input),
		OnTitle:    func(s string) { glfw.PostEmptyEvent(); a.title.Store(&s) },
	})
	go a.writeLoop()
	win.SetCharModsCallback(a.onChar)
	win.SetKeyCallback(a.onKey)
	win.SetFramebufferSizeCallback(a.onResize)

	go a.readLoop()

	a.draw()
	for !win.ShouldClose() && !a.exited.Load() {
		glfw.WaitEvents() // blocks: zero CPU while idle
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

func gridSize(w, h int) (cols, rows uint16) {
	return uint16(max(1, w/cellW)), uint16(max(1, h/cellH))
}

// readLoop drains child output on its own goroutine.
func (a *App) readLoop() {
	buf := make([]byte, 64*1024)
	for {
		n, err := a.sess.Read(buf)
		if n > 0 {
			a.log(buf[:n])
			a.term.Write(buf[:n])
			glfw.PostEmptyEvent() // wake the main loop to redraw
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
	// Tokyo-Night-ish background until the renderer and themes exist.
	gl.ClearColor(0x1a/255.0, 0x1b/255.0, 0x26/255.0, 1)
	gl.Clear(gl.COLOR_BUFFER_BIT)
	a.win.SwapBuffers()
}

func (a *App) onResize(_ *glfw.Window, w, h int) {
	gl.Viewport(0, 0, int32(w), int32(h))
	cols, rows := gridSize(w, h)
	a.term.Resize(int(cols), int(rows))
	if err := a.sess.Resize(cols, rows); err != nil {
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

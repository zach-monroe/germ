// Package app owns the window, the main loop, and wires the PTY to input/output.
//
// Phase 0: no VT parsing or text rendering yet. Child output is forwarded to
// a debug writer (normally germ's own stdout) and the window is just a
// cleared GL surface that accepts keyboard input.
package app

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"sync/atomic"

	"github.com/go-gl/gl/v3.3-core/gl"
	"github.com/go-gl/glfw/v3.3/glfw"

	"github.com/zach-monroe/germ/internal/pty"
)

// Placeholder cell metrics until the font package exists (Phase 2).
const (
	cellW = 8
	cellH = 16
)

// Options configures a run.
type Options struct {
	Command []string  // child argv, e.g. ["/usr/bin/fish"]
	Out     io.Writer // where child output is logged
	Quote   bool      // log output as Go-quoted strings (shows escape sequences)
}

type App struct {
	opts   Options
	win    *glfw.Window
	sess   *pty.Session
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

	a := &App{opts: opts, win: win, sess: sess}
	win.SetCharModsCallback(a.onChar)
	win.SetKeyCallback(a.onKey)
	win.SetFramebufferSizeCallback(a.onResize)

	go a.readLoop()

	a.draw()
	for !win.ShouldClose() && !a.exited.Load() {
		glfw.WaitEvents() // blocks: zero CPU while idle
		a.draw()
	}
	return nil
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
	if a.opts.Out == nil {
		return
	}
	if a.opts.Quote {
		fmt.Fprintln(a.opts.Out, strconv.Quote(string(p)))
		return
	}
	a.opts.Out.Write(p)
}

func (a *App) send(p []byte) {
	if _, err := a.sess.Write(p); err != nil {
		fmt.Fprintln(os.Stderr, "germ: write to pty:", err)
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

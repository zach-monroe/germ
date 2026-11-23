package app

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"sync/atomic"

	"github.com/go-gl/glfw/v3.3/glfw"

	"github.com/zach-monroe/germ/internal/pty"
	"github.com/zach-monroe/germ/internal/render"
	"github.com/zach-monroe/germ/internal/term"
)

// backend is where a pane's input goes: a local PTY, or a tmux pane in
// control mode.
type backend interface {
	Write(p []byte) // queue input; must not block the UI thread
	Resize(cols, rows int)
	Close() // end the pane (kill the process / tmux pane)
	Cwd() string
}

// pane is one terminal in a tab.
type pane struct {
	id   int
	term *term.Terminal
	be   backend
	surf *render.Surface
	rect cellRect // position in the content grid

	title  atomic.Pointer[string]
	exited atomic.Bool

	// Main-thread view state.
	offset     int
	lastPushed int64
	scrollAcc  float64
	sel        selection
	link       render.Link
	linkURL    string
}

func (p *pane) send(b []byte) {
	if len(b) > 0 {
		p.be.Write(b)
	}
}

// cwd returns the pane's working directory: what the shell reported via
// OSC 7, else what /proc says.
func (p *pane) cwd() string {
	p.term.Lock()
	d := p.term.CWD()
	p.term.Unlock()
	if d != "" {
		return d
	}
	return p.be.Cwd()
}

func (p *pane) titleString() string {
	if t := p.title.Load(); t != nil && *t != "" {
		return *t
	}
	return "germ"
}

// ---- local PTY backend ------------------------------------------------------

type ptyBackend struct {
	sess  *pty.Session
	input chan []byte
}

func (b *ptyBackend) Write(p []byte) {
	select {
	case b.input <- append([]byte(nil), p...):
	default:
		fmt.Fprintln(os.Stderr, "germ: input queue full, dropping input")
	}
}

func (b *ptyBackend) Resize(cols, rows int) { _ = b.sess.Resize(uint16(cols), uint16(rows)) }
func (b *ptyBackend) Close()                { b.sess.Close() }
func (b *ptyBackend) Cwd() string           { return b.sess.Cwd() }

// writeLoop is the only writer to the PTY, preserving the order of
// keystrokes and query replies.
func (b *ptyBackend) writeLoop() {
	for p := range b.input {
		if _, err := b.sess.Write(p); err != nil {
			return
		}
	}
}

// writerFunc adapts a function to io.Writer (for term.Options.Responder).
type writerFunc func([]byte)

func (f writerFunc) Write(p []byte) (int, error) { f(p); return len(p), nil }

// newTerm creates a terminal wired to the app's callbacks for pane p.
func (a *App) newTerm(p *pane, cols, rows int) *term.Terminal {
	return a.makeTerm(p, cols, rows, writerFunc(p.send))
}

// newTermNoReplies is newTerm for panes whose host (tmux) answers
// terminal queries itself.
func (a *App) newTermNoReplies(p *pane, cols, rows int) *term.Terminal {
	return a.makeTerm(p, cols, rows, nil)
}

func (a *App) makeTerm(p *pane, cols, rows int, resp io.Writer) *term.Terminal {
	th, _ := a.cfg.ResolveTheme()
	return term.New(cols, rows, term.Options{
		Scrollback: a.cfg.Scrollback.Lines,
		Theme:      th,
		Responder:  resp,
		OnTitle: func(s string) {
			p.title.Store(&s)
			glfw.PostEmptyEvent()
		},
		OnCommandDone: a.onCommandDone,
		OnClipboard:   a.onClipboard,
	})
}

// newLocalPane starts argv in a PTY of the given size.
func (a *App) newLocalPane(argv []string, dir string, cols, rows int) (*pane, error) {
	cols, rows = max(cols, 1), max(rows, 1)
	env := append([]string(nil), a.baseEnv...)
	env = append(env, "GERM_PANE_ID="+strconv.Itoa(a.nextID))
	if a.cfg.Shells.Mode != "off" {
		env = append(env, shellintEnv(argv)...)
	}
	sess, err := pty.StartIn(dir, argv, uint16(cols), uint16(rows), env...)
	if err != nil {
		return nil, err
	}
	p := a.addPane()
	be := &ptyBackend{sess: sess, input: make(chan []byte, 256)}
	p.be = be
	p.term = a.newTerm(p, cols, rows)
	go be.writeLoop()
	go a.readLoop(p, sess)
	return p, nil
}

// addPane allocates a pane with a fresh ID and surface.
func (a *App) addPane() *pane {
	a.nextID++
	p := &pane{id: a.nextID, surf: a.render.NewSurface()}
	a.panes[p.id] = p
	return p
}

// readLoop drains a PTY on its own goroutine.
func (a *App) readLoop(p *pane, sess *pty.Session) {
	buf := make([]byte, 64*1024)
	for {
		n, err := sess.Read(buf)
		if n > 0 {
			a.log(buf[:n])
			p.term.Write(buf[:n])
			a.wake()
		}
		if err != nil {
			// EIO is the normal "child exited" signal from a PTY master.
			p.exited.Store(true)
			a.paneExited.Store(true)
			glfw.PostEmptyEvent()
			return
		}
	}
}

// wake asks the main loop to redraw, at most once per frame: the X11
// wakeup is a cgo call plus a round-trip to the server.
func (a *App) wake() {
	if !a.wakePending.Swap(true) {
		glfw.PostEmptyEvent()
	}
}

package app

import (
	"fmt"
	"os/exec"
	"time"

	"github.com/go-gl/glfw/v3.3/glfw"

	"github.com/zach-monroe/germ/internal/config"
)

// notifyAfter is how long a command must run before germ notifies you
// that it finished while the window was unfocused.
const notifyAfter = 10 * time.Second

// doExtended handles shell-integration actions and (later) tabs and
// splits. Unhandled actions return false so the key reaches the app.
func (a *App) doExtended(act config.Action) bool {
	switch act {
	case config.ActionPrevPrompt:
		return a.jumpPrompt(-1)
	case config.ActionNextPrompt:
		return a.jumpPrompt(1)
	case config.ActionCopyLastOutput:
		a.term.Lock()
		sr, sc, er, ec, ok := a.term.LastOutput()
		text := ""
		if ok {
			text = a.term.TextRange(sr, sc, er, ec)
		}
		a.term.Unlock()
		if ok {
			glfw.SetClipboardString(text)
		}
		return true
	}
	return false
}

// jumpPrompt scrolls so the previous (dir<0) or next (dir>0) prompt is at
// the top of the viewport. Requires shell integration marks.
func (a *App) jumpPrompt(dir int) bool {
	a.term.Lock()
	defer a.term.Unlock()
	if a.term.AltScreen() {
		return false
	}
	sb := a.term.ScrollbackLen()
	top := a.term.AbsRow(0, a.offset)
	oldest := a.term.AbsRow(0, sb)
	live := a.term.AbsRow(0, 0)
	_, rows := a.term.Size()
	for row := top + int64(dir); row >= oldest && row < live+int64(rows); row += int64(dir) {
		if l := a.term.LineAt(row); l != nil && l.Prompt {
			off := int(live - row)
			a.offset = max(0, min(off, sb))
			a.forceRedraw = true
			return true
		}
	}
	if dir > 0 { // past the last prompt: back to the live view
		a.offset = 0
		a.forceRedraw = true
	}
	return true
}

// onCommandDone runs on the reader goroutine (terminal lock held) when the
// shell reports a finished command.
func (a *App) onCommandDone(exit int, d time.Duration) {
	if d < notifyAfter || a.focusedA.Load() {
		return
	}
	a.attention.Store(true)
	glfw.PostEmptyEvent()
	body := fmt.Sprintf("finished after %s", d.Round(time.Second))
	if exit != 0 {
		body = fmt.Sprintf("failed (exit %d) after %s", exit, d.Round(time.Second))
	}
	go func() {
		if p, err := exec.LookPath("notify-send"); err == nil {
			_ = exec.Command(p, "--app-name=germ", "Command "+body).Run()
		}
	}()
}

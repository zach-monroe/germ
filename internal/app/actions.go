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
	case config.ActionNewTab:
		a.actionNewTab()
		return true
	case config.ActionCloseTab:
		a.actionCloseTab()
		return true
	case config.ActionNextTab:
		a.switchTab(a.cur + 1)
		return true
	case config.ActionPrevTab:
		a.switchTab(a.cur - 1)
		return true
	case config.ActionSplitRight:
		a.actionSplit(splitH)
		return true
	case config.ActionSplitDown:
		a.actionSplit(splitV)
		return true
	case config.ActionClosePane:
		a.actionClosePane()
		return true
	case config.ActionFocusLeft:
		return a.actionFocus(dirLeft)
	case config.ActionFocusRight:
		return a.actionFocus(dirRight)
	case config.ActionFocusUp:
		return a.actionFocus(dirUp)
	case config.ActionFocusDown:
		return a.actionFocus(dirDown)
	case config.ActionZoomPane:
		a.actionZoom()
		return true
	case config.ActionSendToAgent:
		return a.sendToAgent()
	case config.ActionSearch:
		a.closeHints()
		return a.openSearch()
	case config.ActionHints:
		return a.openHints()
	case config.ActionReview:
		a.openReview()
		return true
	case config.ActionPalette:
		return a.openPalette()
	case config.ActionChat:
		return a.toggleChat()
	case config.ActionHelm:
		return a.helmKey()
	case config.ActionFollow:
		return a.toggleFollow()
	case config.ActionShare:
		a.shareSession()
		return true
	case config.ActionHome:
		return a.toggleHome()
	case config.ActionNewAgent:
		a.newAgent("")
		return true
	case config.ActionDetach:
		if a.tmux != nil {
			a.quitting = true // tmux keeps the session; reattach later
			return true
		}
		return false
	case config.ActionPrevPrompt:
		return a.jumpPrompt(-1)
	case config.ActionNextPrompt:
		return a.jumpPrompt(1)
	case config.ActionCopyLastOutput:
		p := a.active()
		if p == nil {
			return false
		}
		p.term.Lock()
		sr, sc, er, ec, ok := p.term.LastOutput()
		text := ""
		if ok {
			text = p.term.TextRange(sr, sc, er, ec)
		}
		p.term.Unlock()
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
	p := a.active()
	if p == nil {
		return false
	}
	p.term.Lock()
	defer p.term.Unlock()
	if p.term.AltScreen() {
		return false
	}
	sb := p.term.ScrollbackLen()
	top := p.term.AbsRow(0, p.offset)
	oldest := p.term.AbsRow(0, sb)
	live := p.term.AbsRow(0, 0)
	_, rows := p.term.Size()
	for row := top + int64(dir); row >= oldest && row < live+int64(rows); row += int64(dir) {
		if l := p.term.LineAt(row); l != nil && l.Prompt {
			off := int(live - row)
			p.offset = max(0, min(off, sb))
			a.forceRedraw = true
			return true
		}
	}
	if dir > 0 { // past the last prompt: back to the live view
		p.offset = 0
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

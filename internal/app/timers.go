package app

import (
	"math"
	"time"

	"github.com/zach-monroe/germ/internal/term"
)

// Things that need the main loop to wake on a timer rather than only on
// events: agent spinners, the blinking cursor, and scrolling while a
// selection is dragged past a pane's edge.

const blinkPeriod = 530 * time.Millisecond

// blinking reports whether p's program asked for a blinking cursor.
func blinking(p *pane) bool {
	p.term.Lock()
	defer p.term.Unlock()
	switch p.term.CursorStyle() {
	case term.CursorBlinkBlock, term.CursorBlinkUnderline, term.CursorBlinkBar:
		return true
	}
	return p.term.Modes().CursorBlink
}

// blinkOff reports whether p's cursor is in the off phase of its blink.
// The cursor stays solid while you type or output arrives.
func (a *App) blinkOff(p *pane) bool {
	if !a.focused || !blinking(p) {
		return false
	}
	since := time.Since(p.lastActivity())
	return since > blinkPeriod && (since/blinkPeriod)%2 == 1
}

// lastActivity is when the pane last had input or output.
func (p *pane) lastActivity() time.Time {
	t := max(p.agent.lastInput.Load(), p.agent.lastOutput.Load())
	return time.Unix(0, t)
}

// timerWait is how long the main loop may sleep before something on a
// timer needs drawing (0: wait for events only).
func (a *App) timerWait() time.Duration {
	var wait time.Duration
	soonest := func(d time.Duration) {
		if d > 0 && (wait == 0 || d < wait) {
			wait = d
		}
	}
	if a.animating {
		soonest(100 * time.Millisecond)
	}
	if a.autoscroll() {
		soonest(50 * time.Millisecond)
	}
	if p := a.active(); p != nil && a.focused && blinking(p) {
		since := time.Since(p.lastActivity())
		soonest(blinkPeriod - since%blinkPeriod + time.Millisecond)
	}
	return wait
}

// autoscroll scrolls a pane while a selection is dragged above or below
// it, faster the further out the pointer is. It reports whether it's
// scrolling (so the loop keeps ticking).
func (a *App) autoscroll() bool {
	p := a.mouse.capture
	if p == nil || !p.sel.dragging {
		return false
	}
	// The pointer's row, unclamped: above or below the window counts.
	_, ch := a.render.CellSize()
	gy := int(math.Floor((a.mouse.py - float64(a.render.Pad())) / float64(ch)))
	y := gy - a.barRows() - p.rect.Y
	var lines int
	switch {
	case y < 0:
		lines = 1 + (-y-1)/2
	case y >= p.rect.H:
		lines = -(1 + (y-p.rect.H)/2)
	default:
		return false
	}
	p.term.Lock()
	sb := p.term.ScrollbackLen()
	p.term.Unlock()
	off := max(0, min(p.offset+lines, sb))
	if off == p.offset {
		return true // at the end; keep watching in case the pointer moves
	}
	p.offset = off
	row := 0
	if lines < 0 {
		row = p.rect.H - 1
	}
	col := max(0, min(a.mouse.gx-p.rect.X, p.rect.W-1))
	p.sel.head = absPoint(p, col, row)
	p.sel.active = true
	a.forceRedraw = true
	return true
}

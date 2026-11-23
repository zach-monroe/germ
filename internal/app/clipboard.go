package app

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/go-gl/glfw/v3.3/glfw"

	"github.com/zach-monroe/germ/internal/input"
)

// The CLIPBOARD selection goes through GLFW. GLFW 3.3 has no access to
// the X11 PRIMARY selection (select-to-copy, middle-click paste), so that
// uses whichever helper is installed.

type primaryTool struct {
	copy, paste []string
}

func findPrimaryTool() *primaryTool {
	candidates := []primaryTool{
		{[]string{"xclip", "-selection", "primary", "-in"}, []string{"xclip", "-selection", "primary", "-out"}},
		{[]string{"xsel", "--primary", "--input"}, []string{"xsel", "--primary", "--output"}},
	}
	if os.Getenv("WAYLAND_DISPLAY") != "" && os.Getenv("DISPLAY") == "" {
		candidates = append([]primaryTool{{[]string{"wl-copy", "--primary"}, []string{"wl-paste", "--primary", "--no-newline"}}}, candidates...)
	}
	for _, c := range candidates {
		if _, err := exec.LookPath(c.copy[0]); err == nil {
			return &c
		}
	}
	return nil
}

var primary = findPrimaryTool()

// setPrimary publishes text as the PRIMARY selection, in the background.
func setPrimary(text string) {
	if primary == nil || text == "" {
		return
	}
	go func() {
		cmd := exec.Command(primary.copy[0], primary.copy[1:]...)
		cmd.Stdin = strings.NewReader(text)
		_ = cmd.Run() // xclip/xsel fork a server process and return
	}()
}

func getPrimary() (string, bool) {
	if primary == nil {
		return "", false
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, primary.paste[0], primary.paste[1:]...).Output()
	if err != nil {
		return "", false
	}
	return string(out), true
}

func (a *App) copySelection() {
	p := a.active()
	if p == nil {
		return
	}
	if text := p.sel.text(p.term); text != "" {
		glfw.SetClipboardString(text)
	}
}

// pastePrimary pastes the PRIMARY selection. The helper runs off the main
// thread so a slow selection owner can't freeze the window.
func (a *App) pastePrimary() {
	p := a.active()
	if p == nil {
		return
	}
	if primary == nil {
		a.pasteClipboard()
		return
	}
	if p.offset != 0 {
		p.offset = 0
		a.forceRedraw = true
	}
	go func() {
		if text, ok := getPrimary(); ok {
			p.send(encodePasteFor(p, text))
		}
	}()
}

func (a *App) paste(text string) {
	if text != "" {
		a.sendUser(a.encodePaste(text))
	}
}

func (a *App) encodePaste(text string) []byte {
	p := a.active()
	if p == nil {
		return nil
	}
	return encodePasteFor(p, text)
}

// encodePasteFor is safe from any goroutine.
func encodePasteFor(p *pane, text string) []byte {
	p.term.Lock()
	bracketed := p.term.Modes().BracketedPaste
	p.term.Unlock()
	return input.EncodePaste(text, bracketed)
}

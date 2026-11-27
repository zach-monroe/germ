package app

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-gl/glfw/v3.3/glfw"

	"github.com/zach-monroe/germ/internal/desktop"
	"github.com/zach-monroe/germ/internal/input"
)

// Input conveniences for coding agents: Shift+Enter newlines, pasting
// images and files as paths, and sending text to an agent.

// agentShiftEnter returns the bytes for Shift+Enter in p, or nil to use
// the normal encoding. Without the kitty keyboard protocol (or
// modifyOtherKeys), Shift+Enter is indistinguishable from Enter, so in
// agent panes germ sends ESC CR, which agent UIs (Claude Code, Codex, ...)
// treat as "insert a newline".
func (a *App) agentShiftEnter(p *pane, ev input.KeyEvent, st input.State) []byte {
	if !a.cfg.Agents.ShiftEnter || p == nil || p.agent.name == "" {
		return nil
	}
	if ev.Key != input.KeyEnter || ev.Mods != input.ModShift || ev.Action == input.Release {
		return nil
	}
	if st.KittyFlags != 0 || st.ModifyOtherKeys != 0 {
		return nil // the app can already tell Shift+Enter apart
	}
	return []byte("\x1b\r")
}

// pasteClipboard pastes text, or if the clipboard holds an image, saves it
// and pastes its path (agents attach image paths as images).
func (a *App) pasteClipboard() {
	if text := glfw.GetClipboardString(); text != "" {
		a.paste(text)
		return
	}
	p := a.active()
	if p == nil {
		return
	}
	go func() {
		path, err := saveClipboardImage()
		if err != nil {
			if err != errNoImage {
				fmt.Fprintln(os.Stderr, "germ: paste image:", err)
			}
			return
		}
		p.agent.lastInput.Store(time.Now().UnixNano())
		p.send(encodePasteFor(p, shellQuotePath(path)))
	}()
}

var errNoImage = fmt.Errorf("no image on the clipboard")

// clipboardImageTools are tried in order: list types, then read a type.
var clipboardImageTools = []struct {
	list, read []string
	wayland    bool
}{
	{[]string{"wl-paste", "--list-types"}, []string{"wl-paste", "--no-newline", "--type"}, true},
	{[]string{"xclip", "-selection", "clipboard", "-t", "TARGETS", "-o"}, []string{"xclip", "-selection", "clipboard", "-o", "-t"}, false},
}

// saveClipboardImage writes the clipboard's image to germ's cache and
// returns the file's path.
func saveClipboardImage() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for _, t := range clipboardImageTools {
		if t.wayland && os.Getenv("WAYLAND_DISPLAY") == "" {
			continue
		}
		if _, err := exec.LookPath(t.list[0]); err != nil {
			continue
		}
		out, err := exec.CommandContext(ctx, t.list[0], t.list[1:]...).Output()
		if err != nil {
			continue
		}
		mime := ""
		for _, typ := range strings.Fields(string(out)) {
			if typ == "image/png" {
				mime = typ
				break
			}
			if strings.HasPrefix(typ, "image/") && mime == "" {
				mime = typ
			}
		}
		if mime == "" {
			return "", errNoImage
		}
		data, err := exec.CommandContext(ctx, t.read[0], append(t.read[1:], mime)...).Output()
		if err != nil || len(data) == 0 {
			return "", fmt.Errorf("reading %s: %v", mime, err)
		}
		dir := pasteDir()
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return "", err
		}
		cleanOldPastes(dir)
		ext := strings.TrimPrefix(mime, "image/")
		if ext == "jpeg" {
			ext = "jpg"
		}
		path := filepath.Join(dir, "paste-"+time.Now().Format("20060102-150405.000")+"."+ext)
		return path, os.WriteFile(path, data, 0o600)
	}
	return "", errNoImage
}

func pasteDir() string {
	base := os.Getenv("XDG_CACHE_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".cache")
	}
	return filepath.Join(base, "germ", "paste")
}

// cleanOldPastes removes pasted images older than a week.
func cleanOldPastes(dir string) {
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if fi, err := e.Info(); err == nil && time.Since(fi.ModTime()) > 7*24*time.Hour {
			os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

// shellQuotePath quotes a path only if it needs it.
func shellQuotePath(p string) string {
	if strings.Trim(p, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_./=:,+@%") == "" {
		return p
	}
	return "'" + strings.ReplaceAll(p, "'", `'\''`) + "'"
}

// onDrop pastes dropped files' paths into the pane under the pointer.
func (a *App) onDrop(_ *glfw.Window, names []string) {
	p, _, _, inBar := a.hit()
	if p == nil || inBar {
		p = a.active()
	}
	if p == nil || len(names) == 0 {
		return
	}
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = shellQuotePath(n)
	}
	p.agent.lastInput.Store(time.Now().UnixNano())
	p.send(encodePasteFor(p, strings.Join(quoted, " ")))
}

// sendToAgent sends the selection, or else the last command's output, to
// the agent pane you used most recently, as a fenced block saying where
// it came from, and focuses that pane so you can add your question.
func (a *App) sendToAgent() bool {
	src := a.active()
	if src == nil {
		return false
	}
	text := src.sel.text(src.term)
	what := "selection"
	if text == "" {
		src.term.Lock()
		if r0, c0, r1, c1, ok := src.term.LastOutput(); ok {
			text = src.term.TextRange(r0, c0, r1, c1)
		}
		src.term.Unlock()
		what = "output"
	}
	text = strings.TrimRight(text, "\n ")
	if text == "" {
		return false
	}
	target := a.recentAgent(src)
	if target == nil {
		go desktop.Notify("germ", "No agent pane to send to. Start one with Ctrl+Shift+A.")
		return true
	}
	from := src.titleString()
	if pid := src.be.Pid(); pid > 0 {
		if fg := foregroundPgid(pid); fg > 0 {
			from = procArgs(fg)[0]
		}
	}
	header := fmt.Sprintf("%s from `%s`", what, from)
	if dir := paneDir(src); dir != "" {
		header += " in " + dir
	}
	msg := header + ":\n```\n" + text + "\n```\n"
	target.agent.lastInput.Store(time.Now().UnixNano())
	target.send(encodePasteFor(target, msg))
	src.sel.clear()
	a.focusPane(target)
	a.forceRedraw = true
	return true
}

// recentAgent is the agent pane (other than exclude) you typed into most
// recently.
func (a *App) recentAgent(exclude *pane) *pane {
	var best *pane
	for _, p := range a.panes {
		if p == exclude || p.agent.name == "" {
			continue
		}
		if best == nil || p.agent.lastInput.Load() > best.agent.lastInput.Load() {
			best = p
		}
	}
	return best
}

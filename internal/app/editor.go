package app

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/zach-monroe/germ/internal/control"
)

// openInEditor opens a file reference (Ctrl+click on "path:line") in
// $VISUAL/$EDITOR: terminal editors in a split beside the pane, GUI
// editors on their own.
func (a *App) openInEditor(p *pane, ref *fileRef) {
	argv, gui := editorCommand(editorFromEnv(), ref)
	if gui {
		cmd := exec.Command(argv[0], argv[1:]...)
		if err := cmd.Start(); err != nil {
			fmt.Fprintln(os.Stderr, "germ:", err)
			return
		}
		go cmd.Wait()
		return
	}
	noop := func(*control.Response) {}
	target := p
	if _, widget := p.be.(*widgetBackend); widget {
		target = nil
	}
	a.ctlOpen(control.Request{Where: "right", Command: []string{shellJoin(argv)}, Focus: true,
		Cwd: filepath.Dir(ref.path)}, target, noop, noop)
}

func editorFromEnv() []string {
	for _, k := range []string{"VISUAL", "EDITOR"} {
		if f := strings.Fields(os.Getenv(k)); len(f) > 0 {
			return f
		}
	}
	for _, e := range []string{"nvim", "vim", "vi", "nano"} {
		if _, err := exec.LookPath(e); err == nil {
			return []string{e}
		}
	}
	return []string{"vi"}
}

// editorCommand builds the command to open ref, and whether the editor is
// a GUI app (not run in a pane).
func editorCommand(editor []string, ref *fileRef) (argv []string, gui bool) {
	line, col := max(ref.line, 1), max(ref.col, 1)
	pos := fmt.Sprintf("%s:%d:%d", ref.path, line, col)
	name := filepath.Base(editor[0])
	argv = append([]string(nil), editor...)
	switch name {
	case "code", "codium", "code-insiders", "cursor", "windsurf":
		return append(argv, "-g", pos), true
	case "zed", "subl", "sublime_text":
		return append(argv, pos), true
	case "hx", "helix":
		return append(argv, pos), false
	case "kak":
		return append(argv, fmt.Sprintf("+%d:%d", line, col), ref.path), false
	}
	if ref.line > 0 {
		argv = append(argv, fmt.Sprintf("+%d", line))
	}
	return append(argv, ref.path), false
}

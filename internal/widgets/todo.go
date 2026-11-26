package widgets

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// todo shows a markdown checklist and ticks items off when you click
// them, saving the file: edit it in any editor, sync it however you like.
// Click "+ add" (or click the widget and just type) to add one: Enter
// saves it after the last item, Esc stops.
// Lines like "- [ ] task" are items ("- [x]" done), "# Heading" lines are
// headings, everything else is left out (and left alone). Options:
// file = "~/todo.md", done = "show" | "hide".
type todo struct {
	path     string
	hideDone bool

	mu    sync.Mutex
	lines []string // the whole file
	mtime time.Time
	size  int64
	err   error

	adding bool   // typing a new item
	draft  []rune // what's typed so far
}

// checkbox matches a task line: indent, bullet, box, text.
var checkbox = regexp.MustCompile(`^(\s*)([-*+]|\d+[.)])\s+\[([ xX])\]\s?(.*)$`)

func newTodo(o Options) Widget {
	return &todo{path: expandTilde(o.Get("file", "~/todo.md")), hideDone: o.Get("done", "show") == "hide"}
}

func (t *todo) Every() time.Duration { return time.Second } // a stat: notices edits
func (t *todo) Tick() time.Duration  { return 0 }

func (t *todo) Update(context.Context) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.reload(false)
}

// reload rereads the file if it changed (or always, if force). Caller
// holds t.mu.
func (t *todo) reload(force bool) error {
	st, err := os.Stat(t.path)
	if errors.Is(err, fs.ErrNotExist) {
		t.lines, t.err, t.mtime = nil, nil, time.Time{}
		return nil
	}
	if err != nil {
		t.err = err
		return err
	}
	if !force && st.ModTime().Equal(t.mtime) && st.Size() == t.size {
		return nil
	}
	b, err := os.ReadFile(t.path)
	if err != nil {
		t.err = err
		return err
	}
	t.lines = strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
	t.mtime, t.size, t.err = st.ModTime(), st.Size(), nil
	return nil
}

// lineToken names line i as it is now, so a click on a stale screen
// (the file changed under it) does nothing instead of ticking the wrong
// line.
func lineToken(i int, line string) string {
	h := fnv.New32a()
	h.Write([]byte(line))
	return fmt.Sprintf("toggle:%d:%08x", i, h.Sum32())
}

// Click ticks an item off (or back on) and saves the file, or creates
// the file.
func (t *todo) Click(_ context.Context, token string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if token == "add" {
		t.adding = true
		return
	}
	if token == "create" {
		if _, err := os.Stat(t.path); errors.Is(err, fs.ErrNotExist) {
			os.MkdirAll(filepath.Dir(t.path), 0o755)
			t.err = os.WriteFile(t.path, []byte("# Todo\n\n- [ ] click a box to tick it off\n- [ ] edit this file to add more\n"), 0o644)
		}
		t.reload(true)
		return
	}
	rest, ok := strings.CutPrefix(token, "toggle:")
	if !ok {
		return
	}
	idx, _, _ := strings.Cut(rest, ":")
	i, err := strconv.Atoi(idx)
	if err != nil {
		return
	}
	t.reload(true) // the file as it is now
	if i < 0 || i >= len(t.lines) || lineToken(i, t.lines[i]) != token {
		return // changed since it was drawn
	}
	m := checkbox.FindStringSubmatchIndex(t.lines[i])
	if m == nil {
		return
	}
	box := m[6] // the character inside [ ]
	line := t.lines[i]
	mark := "x"
	if line[box] != ' ' {
		mark = " "
	}
	t.lines[i] = line[:box] + mark + line[box+1:]
	if err := t.save(); err != nil {
		t.err = err
	}
	t.reload(true)
}

// save writes the file back atomically, through a symlink if it is one,
// keeping its permissions.
func (t *todo) save() error {
	path, err := filepath.EvalSymlinks(t.path)
	if errors.Is(err, fs.ErrNotExist) {
		if _, lerr := os.Lstat(t.path); !errors.Is(lerr, fs.ErrNotExist) {
			return err // a dangling symlink: don't replace it
		}
		path = t.path // a new file
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	mode := fs.FileMode(0o644)
	if st, err := os.Stat(path); err == nil {
		mode = st.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".germ-todo-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(strings.Join(t.lines, "\n") + "\n"); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Type edits the new item: text, Backspace, Ctrl+U (clear), Ctrl+W
// (a word), Enter (save it; Enter on nothing stops), Esc (stop). Typing
// while not adding starts adding.
func (t *todo) Type(_ context.Context, b []byte) {
	t.mu.Lock()
	defer t.mu.Unlock()
	s := string(b)
	if s == "\x1b" {
		t.adding, t.draft = false, nil
		return
	}
	if strings.HasPrefix(s, "\x1b") {
		return // arrows, function keys, Alt+…: nothing to do
	}
	if !t.adding {
		if r := []rune(s); len(r) == 0 || (r[0] < 0x20 && r[0] != '\r') || r[0] == 0x7f {
			return
		}
		t.adding = true
		if s == "\r" {
			return // Enter starts adding
		}
	}
	for _, r := range s {
		switch {
		case r == '\r' || r == '\n':
			text := strings.TrimSpace(string(t.draft))
			t.draft = nil
			if text == "" {
				if r == '\r' && len(s) == 1 {
					t.adding = false // Enter on an empty line: done
				}
				continue
			}
			if err := t.add(text); err != nil {
				t.err = err
			}
		case r == 0x7f || r == '\b':
			if len(t.draft) > 0 {
				t.draft = t.draft[:len(t.draft)-1]
			}
		case r == 0x15: // Ctrl+U
			t.draft = nil
		case r == 0x17: // Ctrl+W
			d := strings.TrimRight(string(t.draft), " ")
			t.draft = []rune(d[:strings.LastIndex(d, " ")+1])
		case r == '\t':
			t.draft = append(t.draft, ' ')
		case r >= 0x20:
			t.draft = append(t.draft, r)
		}
	}
}

// add puts "- [ ] text" after the last item (in its style), or at the
// end, and saves. Caller holds t.mu.
func (t *todo) add(text string) error {
	if err := t.reload(true); err != nil {
		return err
	}
	at, bullet := -1, ""
	for i := len(t.lines) - 1; i >= 0 && bullet == ""; i-- {
		m := checkbox.FindStringSubmatch(t.lines[i])
		if m == nil {
			continue
		}
		if at < 0 {
			at = i + 1 // after the last item
		}
		if m[1] == "" { // the last top-level item's bullet
			bullet = m[2]
		}
	}
	if at < 0 {
		at = len(t.lines)
	}
	if !strings.ContainsAny(bullet, "-*+") {
		bullet = "-" // none, or numbered
	}
	item := bullet + " [ ] " + text
	switch {
	case t.lines == nil: // no file yet
		t.lines = []string{"# Todo", "", item}
	case at == len(t.lines) && strings.TrimSpace(t.lines[at-1]) != "" && !checkbox.MatchString(t.lines[at-1]):
		t.lines = append(t.lines, "", item) // after other text: a blank line first
	default:
		t.lines = append(t.lines[:at], append([]string{item}, t.lines[at:]...)...)
	}
	if err := t.save(); err != nil {
		return err
	}
	return t.reload(true)
}

func (t *todo) Draw(w, h int, err error) []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.err != nil {
		err = t.err
	}
	type item struct {
		i            int
		indent, text string
		done         bool
		heading      bool
	}
	var items []item
	left := 0
	for i, l := range t.lines {
		if m := checkbox.FindStringSubmatch(l); m != nil {
			done := m[3] != " "
			if !done {
				left++
			}
			if done && t.hideDone {
				continue
			}
			items = append(items, item{i: i, indent: strings.ReplaceAll(m[1], "\t", "  "), text: m[4], done: done})
		} else if strings.HasPrefix(l, "#") {
			items = append(items, item{i: i, text: strings.TrimSpace(strings.TrimLeft(l, "#")), heading: true})
		}
	}
	info := ""
	if t.lines != nil {
		info = fmt.Sprintf("%d left", left)
		if left == 0 && len(items) > 0 {
			info = "all done ✓"
		}
	}
	if t.adding {
		info = "enter adds · esc stops"
	} else if info != "" {
		info += "  " + reset + click("add", cyan+"+ add"+reset)
	}
	lines := []string{title(w, "☐", "todo", info), ""}
	// The new item being typed, with a cursor, shown after the list.
	var input string
	if t.adding {
		pre := "  ☐ "
		input = pre + truncateLeft(string(t.draft), max(1, w-width(pre)-2)) + "\x1b[7m \x1b[27m"
	}
	if t.lines == nil && t.err == nil && t.adding {
		return fill(append(lines, input), h, errLine(err, w))
	}
	if t.lines == nil && t.err == nil {
		lines = append(lines, dim+"  No "+shortDir(t.path)+" yet."+reset)
		if h > 4 {
			lines = append(lines, "")
		}
		lines = append(lines, "  "+click("create", cyan+"[ create it ]"+reset))
		return fill(lines[:min(len(lines), h)], h, errLine(err, w))
	}
	// Leave out a heading with no items under it before the next heading
	// (or the end), and the first heading if it's just the title.
	var shown []item
	for k, it := range items {
		if it.heading && (k+1 == len(items) || items[k+1].heading) {
			continue
		}
		if it.heading && len(shown) == 0 && strings.EqualFold(it.text, "todo") {
			continue
		}
		shown = append(shown, it)
	}
	if len(shown) == 0 && !t.adding {
		lines = append(lines, dim+"  Nothing to do. Click here and type"+reset, dim+"  to add one."+reset)
		return fill(lines[:min(len(lines), h)], h, errLine(err, w))
	}
	limit := h - boolInt(err != nil) - boolInt(t.adding)
	// While typing, show the end of the list, where the new item goes.
	if t.adding {
		rows := 0
		start := len(shown)
		for start > 0 && rows+1+boolInt(shown[start-1].heading && start-1 > 0) <= limit-len(lines) {
			start--
			rows += 1 + boolInt(shown[start].heading && start > 0)
		}
		shown = shown[start:]
	}
	for k, it := range shown {
		if len(lines) >= limit {
			break
		}
		if it.heading {
			if k > 0 && len(lines) < limit-1 {
				lines = append(lines, "")
			}
			lines = append(lines, " "+bold+truncate(it.text, w-2)+reset)
			continue
		}
		box, text := "☐", it.text
		style := ""
		if it.done {
			box, style = green+"☑"+reset, dim+"\x1b[9m" // struck through
		}
		pre := "  " + it.indent
		row := pre + box + " " + style + truncate(text, max(0, w-width(pre)-3)) + reset
		row += strings.Repeat(" ", max(0, w-width(row))) // the whole row takes the click
		row = click(lineToken(it.i, t.lines[it.i]), row)
		lines = append(lines, row)
	}
	if t.adding {
		lines = append(lines, input)
	}
	return fill(lines, h, errLine(err, w))
}

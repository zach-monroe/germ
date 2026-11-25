package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/sys/unix"

	"github.com/zach-monroe/germ/internal/control"
)

// `germ review` (Ctrl+Shift+R) lists every agent worktree of the current
// repository (the ones `germ agent` / Ctrl+Shift+A made) with what its
// agent is doing and what it changed, and lets you review, send feedback
// to, commit, merge, or discard each one from a single pane.

type reviewItem struct {
	dir, branch, task string
	base, from        string // start commit, branch it came from
	ahead             int
	commits           []string // "abc1234 subject", newest first
	files             []fileStat
	untracked         []string
	dirty             int
	added, removed    int
	agent             *control.PaneInfo // the agent's pane, if running
	panes             []control.PaneInfo
}

type fileStat struct {
	path           string
	added, removed int
}

func runReview(args []string) int {
	repo, err := mainRepo(".")
	if err != nil {
		fmt.Fprintln(os.Stderr, "germ review:", err)
		return 1
	}
	rv := &reviewer{repo: repo}
	if c, err := control.Connect(); err == nil {
		rv.ctl = c
		rv.self = c.Self()
		defer c.Close()
	}
	if err := rv.loop(); err != nil {
		fmt.Fprintln(os.Stderr, "germ review:", err)
		return 1
	}
	return 0
}

// mainRepo finds the main checkout of the repository dir is in (also
// from inside one of its worktrees).
func mainRepo(dir string) (string, error) {
	common, err := gitOut(dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", errors.New("not in a git repository")
	}
	if filepath.Base(common) != ".git" {
		return "", errors.New("bare repositories aren't supported")
	}
	return filepath.Dir(common), nil
}

// loadReview gathers the agent worktrees and their state.
func loadReview(repo string, panes []control.PaneInfo) []reviewItem {
	out, _ := gitOut(repo, "worktree", "list", "--porcelain")
	var items []reviewItem
	var cur reviewItem
	flush := func() {
		if strings.HasPrefix(cur.branch, "germ/") && cur.dir != "" {
			items = append(items, cur)
		}
		cur = reviewItem{}
	}
	for _, l := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(l, "worktree "):
			flush()
			cur.dir = strings.TrimPrefix(l, "worktree ")
		case strings.HasPrefix(l, "branch refs/heads/"):
			cur.branch = strings.TrimPrefix(l, "branch refs/heads/")
		}
	}
	flush()
	for i := range items {
		it := &items[i]
		cfg := func(k string) string { v, _ := gitOut(repo, "config", "branch."+it.branch+"."+k); return v }
		it.task, it.base, it.from = cfg("germ-task"), cfg("germ-base"), cfg("germ-from")
		if it.from == "" {
			it.from, _ = gitOut(repo, "rev-parse", "--abbrev-ref", "HEAD")
		}
		if it.base == "" {
			it.base, _ = gitOut(repo, "merge-base", it.from, it.branch)
		}
		if it.task == "" {
			it.task = strings.ReplaceAll(strings.TrimPrefix(it.branch, "germ/"), "-", " ")
		}
		if n, err := gitOut(it.dir, "rev-list", "--count", it.base+"..HEAD"); err == nil {
			it.ahead, _ = strconv.Atoi(n)
		}
		if log, _ := gitOut(it.dir, "log", "--format=%h %s", it.base+"..HEAD"); log != "" {
			it.commits = strings.Split(log, "\n")
		}
		if num, _ := gitOut(it.dir, "diff", "--numstat", it.base); num != "" {
			for _, l := range strings.Split(num, "\n") {
				f := strings.SplitN(l, "\t", 3)
				if len(f) < 3 {
					continue
				}
				a, _ := strconv.Atoi(f[0]) // "-" for binary files
				r, _ := strconv.Atoi(f[1])
				it.files = append(it.files, fileStat{f[2], a, r})
				it.added += a
				it.removed += r
			}
		}
		if u, _ := gitOut(it.dir, "ls-files", "--others", "--exclude-standard"); u != "" {
			it.untracked = strings.Split(u, "\n")
		}
		if st, _ := gitOut(it.dir, "status", "--porcelain"); st != "" {
			it.dirty = len(strings.Split(st, "\n"))
		}
		for _, p := range panes {
			if p.Cwd == it.dir || strings.HasPrefix(p.Cwd, it.dir+"/") {
				it.panes = append(it.panes, p)
				if p.Agent != "" && it.agent == nil {
					pp := p
					it.agent = &pp
				}
			}
		}
	}
	return items
}

type reviewer struct {
	repo   string
	ctl    *control.Client
	self   string
	items  []reviewItem
	sel    int
	status string
	tty    *unix.Termios
}

func (rv *reviewer) refresh() {
	var panes []control.PaneInfo
	if rv.ctl != nil {
		if resp, err := rv.ctl.Call(control.Request{Cmd: control.CmdListPanes}); err == nil {
			panes = resp.Panes
		}
	}
	rv.items = loadReview(rv.repo, panes)
	rv.sel = max(0, min(rv.sel, len(rv.items)-1))
}

func (rv *reviewer) raw() {
	if rv.tty == nil {
		return
	}
	raw := *rv.tty
	raw.Lflag &^= unix.ICANON | unix.ECHO | unix.ISIG
	raw.Cc[unix.VMIN], raw.Cc[unix.VTIME] = 1, 0
	unix.IoctlSetTermios(0, unix.TCSETS, &raw)
	fmt.Print("\x1b[?1049h\x1b[?25l")
}

func (rv *reviewer) cooked() {
	fmt.Print("\x1b[?25h\x1b[?1049l")
	if rv.tty != nil {
		unix.IoctlSetTermios(0, unix.TCSETS, rv.tty)
	}
}

func (rv *reviewer) loop() error {
	t, err := unix.IoctlGetTermios(0, unix.TCGETS)
	if err != nil {
		return errors.New("needs a terminal")
	}
	rv.tty = t
	rv.raw()
	defer rv.cooked()
	rv.refresh()
	for {
		rv.draw()
		key, ok := pollKey(2 * time.Second)
		if !ok {
			rv.refresh() // every two seconds
			continue
		}
		rv.status = ""
		switch key {
		case "q", "\x1b", "\x03", "\x04":
			return nil
		case "j", "\x1b[B", "\x1bOB":
			rv.sel = min(rv.sel+1, len(rv.items)-1)
		case "k", "\x1b[A", "\x1bOA":
			rv.sel = max(rv.sel-1, 0)
		case "g":
			rv.refresh()
		default:
			if len(rv.items) == 0 {
				continue
			}
			it := rv.items[rv.sel]
			switch key {
			case "\r", "r":
				rv.showDiff(it)
			case "f":
				rv.feedback(it)
			case "o":
				rv.goTo(it)
			case "c":
				rv.commit(it)
			case "m":
				rv.merge(it)
			case "d":
				rv.discard(it)
			}
			rv.refresh()
		}
	}
}

// pollKey waits up to d for a key press (an escape sequence counts as one).
func pollKey(d time.Duration) (string, bool) {
	fds := []unix.PollFd{{Fd: 0, Events: unix.POLLIN}}
	n, err := unix.Poll(fds, int(d.Milliseconds()))
	if err != nil || n == 0 {
		return "", false
	}
	var b [16]byte
	m, _ := os.Stdin.Read(b[:])
	if m == 0 {
		return "\x04", true
	}
	return string(b[:m]), true
}

// ---- drawing --------------------------------------------------------------

func termSize() (cols, rows int) {
	ws, err := unix.IoctlGetWinsize(1, unix.TIOCGWINSZ)
	if err != nil || ws.Col == 0 {
		return 80, 24
	}
	return int(ws.Col), int(ws.Row)
}

// fit cuts s (which may hold escape sequences) to n visible columns.
func fit(s string, n int) string {
	var b strings.Builder
	w := 0
	for i := 0; i < len(s); {
		if s[i] == '\x1b' {
			j := strings.IndexByte(s[i:], 'm')
			if j < 0 {
				break
			}
			b.WriteString(s[i : i+j+1])
			i += j + 1
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if w >= n {
			break
		}
		b.WriteRune(r)
		w++
		i += size
	}
	return b.String() + reset
}

func tildePath(p string) string {
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(p, home+"/") {
		return "~" + p[len(home):]
	}
	return p
}

// pad right-pads s (which may hold escape sequences) to n visible columns.
func pad(s string, n int) string {
	w := 0
	for i := 0; i < len(s); {
		if s[i] == '\x1b' {
			if j := strings.IndexByte(s[i:], 'm'); j >= 0 {
				i += j + 1
				continue
			}
		}
		_, size := utf8.DecodeRuneInString(s[i:])
		i += size
		w++
	}
	if w >= n {
		return s
	}
	return s + strings.Repeat(" ", n-w)
}

func stateBadge(p *control.PaneInfo) string {
	if p == nil {
		return dim + "○ no agent running" + reset
	}
	switch p.State {
	case "working":
		return cyan + "⠿ working" + reset
	case "waiting":
		return bold + red + "● needs you" + reset
	case "done":
		return green + "✓ done" + reset
	}
	return dim + "· " + p.Agent + " idle" + reset
}

func (rv *reviewer) draw() {
	cols, rows := termSize()
	var lines []string
	add := func(s string) { lines = append(lines, s) }
	add(fmt.Sprintf("%s◆ germ review%s %s· %s%s", bold+blue, reset, dim, tildePath(rv.repo), reset))
	add("")
	if len(rv.items) == 0 {
		add("  No agent worktrees in this repository.")
		add(dim + "  Ctrl+Shift+A (germ agent) starts an agent on its own branch; it shows up here." + reset)
	}
	for i, it := range rv.items {
		mark := "  "
		if i == rv.sel {
			mark = bold + blue + "▸ " + reset
		}
		change := fmt.Sprintf("%s+%d%s %s-%d%s", green, it.added, reset, red, it.removed, reset)
		if len(it.untracked) > 0 {
			change += fmt.Sprintf(" %s%d new%s", yel, len(it.untracked), reset)
		}
		name := it.task
		if r := []rune(name); len(r) > 40 {
			name = string(r[:39]) + "…"
		}
		commits := fmt.Sprintf("%d commits", it.ahead)
		if it.ahead == 1 {
			commits = "1 commit"
		}
		add(fmt.Sprintf("%s%s%s%s  %s  %s  %s", mark, bold, pad(name, 40), reset, pad(stateBadge(it.agent), 20), pad(commits, 10), change))
	}
	if len(rv.items) > 0 {
		it := rv.items[rv.sel]
		add("")
		add(fmt.Sprintf("  %s%s%s → %s   %s%s%s", cyan, it.branch, reset, it.from, dim, tildePath(it.dir), reset))
		if it.agent != nil && it.agent.Message != "" {
			add(fmt.Sprintf("  %s“%s”%s", yel, it.agent.Message, reset))
		}
		if it.dirty > 0 {
			add(fmt.Sprintf("  %s%d uncommitted changes%s", yel, it.dirty, reset))
		}
		for i, c := range it.commits {
			if i == 5 {
				add(fmt.Sprintf("  %s… %d more commits%s", dim, len(it.commits)-5, reset))
				break
			}
			add("  " + dim + c[:min(len(c), 7)] + reset + c[min(len(c), 7):])
		}
		room := rows - len(lines) - 3
		for i, f := range it.files {
			if i >= room-1 && len(it.files) > room {
				add(fmt.Sprintf("  %s… %d more files%s", dim, len(it.files)-i, reset))
				break
			}
			add(fmt.Sprintf("  %s+%-4d%s %s-%-4d%s %s", green, f.added, reset, red, f.removed, reset, f.path))
		}
		for _, u := range it.untracked {
			if len(lines) >= rows-3 {
				break
			}
			add(fmt.Sprintf("  %snew%s        %s", yel, reset, u))
		}
	}
	var b strings.Builder
	b.WriteString("\x1b[H")
	for i, l := range lines {
		if i >= rows-2 {
			break
		}
		b.WriteString(fit(l, cols) + "\x1b[K\r\n")
	}
	b.WriteString("\x1b[J")
	help := "↑↓ select · Enter diff · f feedback · o go to agent · c commit · m merge · d discard · q quit"
	fmt.Fprintf(&b, "\x1b[%d;1H%s\x1b[K", rows-1, fit(rv.status, cols))
	fmt.Fprintf(&b, "\x1b[%d;1H%s\x1b[K", rows, fit(dim+help, cols))
	fmt.Print(b.String())
}

// ---- actions --------------------------------------------------------------

// ask shows a question on the status line and reads one key.
func (rv *reviewer) ask(q string) bool {
	rv.status = bold + yel + q + " [y/N]" + reset
	rv.draw()
	k, _ := pollKey(time.Hour)
	return k == "y" || k == "Y"
}

func (rv *reviewer) showDiff(it reviewItem) {
	rv.cooked()
	defer rv.raw()
	pager, _ := gitOut(it.dir, "var", "GIT_PAGER")
	if pager == "" {
		pager = "less -R"
	}
	script := `git -c color.ui=always diff "$1"; git ls-files -z --others --exclude-standard | ` +
		`xargs -0 -r -n1 git -c color.ui=always diff --no-index /dev/null; true`
	cmd := exec.Command("/bin/sh", "-c", "{ "+script+"; } 2>&1 | "+pager, "sh", it.base)
	cmd.Dir, cmd.Stdin, cmd.Stdout, cmd.Stderr = it.dir, os.Stdin, os.Stdout, os.Stderr
	cmd.Env = append(os.Environ(), "LESS=FRX")
	cmd.Run()
}

func (rv *reviewer) feedback(it reviewItem) {
	if it.agent == nil {
		rv.status = yel + "No agent is running in this worktree (o opens it, or start one there)." + reset
		return
	}
	text := rv.readLine(fmt.Sprintf("Tell %s: ", it.agent.Agent))
	if strings.TrimSpace(text) == "" {
		return
	}
	ref := strconv.Itoa(it.agent.ID)
	if it.agent.Tmux != "" {
		ref = it.agent.Tmux
	}
	if _, err := rv.ctl.Call(control.Request{Cmd: control.CmdSendText, Pane: ref, Text: text, Enter: true}); err != nil {
		rv.status = red + err.Error() + reset
		return
	}
	rv.status = green + "sent to " + it.agent.Agent + reset
}

// readLine reads a line of text on the status row.
func (rv *reviewer) readLine(prompt string) string {
	_, rows := termSize()
	var buf []rune
	for {
		fmt.Printf("\x1b[%d;1H\x1b[K%s%s%s%s\x1b[?25h", rows-1, bold, prompt, reset, string(buf))
		k, _ := pollKey(time.Hour)
		switch {
		case k == "\r" || k == "\n":
			fmt.Print("\x1b[?25l")
			return string(buf)
		case k == "\x1b" || k == "\x03":
			fmt.Print("\x1b[?25l")
			return ""
		case k == "\x7f" || k == "\b":
			if len(buf) > 0 {
				buf = buf[:len(buf)-1]
			}
		case k == "\x15": // Ctrl+U
			buf = buf[:0]
		case !strings.HasPrefix(k, "\x1b"):
			for _, r := range k {
				if r >= ' ' {
					buf = append(buf, r)
				}
			}
		}
	}
}

func (rv *reviewer) goTo(it reviewItem) {
	if rv.ctl == nil || len(it.panes) == 0 {
		rv.status = yel + "Nothing open in this worktree." + reset
		return
	}
	p := it.panes[0]
	if it.agent != nil {
		p = *it.agent
	}
	ref := strconv.Itoa(p.ID)
	if p.Tmux != "" {
		ref = p.Tmux
	}
	rv.ctl.Call(control.Request{Cmd: control.CmdFocusPane, Pane: ref})
}

func (rv *reviewer) commit(it reviewItem) {
	if it.dirty == 0 {
		rv.status = dim + "Nothing to commit." + reset
		return
	}
	if err := commitAll(it); err != nil {
		rv.status = red + err.Error() + reset
		return
	}
	rv.status = green + "committed: " + it.task + reset
}

func commitAll(it reviewItem) error {
	if _, err := gitOut(it.dir, "add", "-A"); err != nil {
		return err
	}
	if out, err := gitOut(it.dir, "commit", "-q", "-m", it.task); err != nil {
		return fmt.Errorf("commit failed: %v %s", err, out)
	}
	return nil
}

// stopPanes closes the panes open in the worktree (the agent, its diff).
func (rv *reviewer) stopPanes(it reviewItem) {
	if rv.ctl == nil {
		return
	}
	for _, p := range it.panes {
		ref := strconv.Itoa(p.ID)
		if p.Tmux != "" {
			ref = p.Tmux
		}
		if ref == rv.self {
			continue
		}
		rv.ctl.Call(control.Request{Cmd: control.CmdClosePane, Pane: ref})
	}
	time.Sleep(300 * time.Millisecond) // let the processes exit before git touches the files
}

func (rv *reviewer) merge(it reviewItem) {
	if it.ahead == 0 && it.dirty == 0 {
		rv.status = dim + "No changes to merge (d discards it)." + reset
		return
	}
	var extra []string
	if it.dirty > 0 {
		extra = append(extra, fmt.Sprintf("commit %d uncommitted changes", it.dirty))
	}
	if len(it.panes) > 0 {
		extra = append(extra, "close the agent's panes")
	}
	q := fmt.Sprintf("Merge %s into %s", it.branch, it.from)
	if len(extra) > 0 {
		q += " (" + strings.Join(extra, ", ") + ")"
	}
	if !rv.ask(q + "?") {
		rv.status = ""
		return
	}
	rv.stopPanes(it)
	if it.dirty > 0 {
		if err := commitAll(it); err != nil {
			rv.status = red + err.Error() + reset
			return
		}
	}
	if err := mergeBranch(rv.repo, it.branch, it.from, it.task); err != nil {
		rv.status = red + "merge failed: " + err.Error() + reset
		return
	}
	if err := removeWorktreeAt(rv.repo, it.dir, it.branch, false); err != nil {
		rv.status = yel + "merged, but couldn't remove the worktree: " + err.Error() + reset
		return
	}
	rv.status = green + "✓ merged " + it.branch + " into " + it.from + reset
}

func (rv *reviewer) discard(it reviewItem) {
	q := fmt.Sprintf("Delete %s and its worktree, losing its changes", it.branch)
	if len(it.panes) > 0 {
		q += " (closes the agent's panes)"
	}
	if !rv.ask(q + "?") {
		rv.status = ""
		return
	}
	rv.stopPanes(it)
	if err := removeWorktreeAt(rv.repo, it.dir, it.branch, true); err != nil {
		rv.status = red + err.Error() + reset
		return
	}
	rv.status = "discarded " + it.branch
}

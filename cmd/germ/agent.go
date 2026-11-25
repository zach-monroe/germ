package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/zach-monroe/germ/internal/config"
	"github.com/zach-monroe/germ/internal/control"
)

// `germ agent [TASK]` runs a coding agent on its own git worktree and
// branch, with a live diff beside it, then offers to review, merge, keep,
// or discard the work. germ's Ctrl+Shift+A opens a tab running it.
//
// `germ agent -for NAME [TASK]` does the same for a person in a shared
// session: a shell instead of an agent, and once the worktree is ready the
// tab is given to them (they own it and have its helm). The host merges
// their branch with germ review.

const (
	bold  = "\x1b[1m"
	dim   = "\x1b[2m"
	red   = "\x1b[31m"
	green = "\x1b[32m"
	yel   = "\x1b[33m"
	blue  = "\x1b[34m"
	cyan  = "\x1b[36m"
	reset = "\x1b[0m"
)

type agentRun struct {
	cfg      config.Agents
	task     string
	repo     string // main checkout
	dir      string // worktree ("" = running in place)
	branch   string
	base     string // commit the branch started from
	baseName string // branch checked out in the main repo at start
	ctl      *control.Client
	self     string // our pane
	diffPane string
	person   string // -for: who the tab is for
	slugFor  string // -for: the branch name
}

func runAgent(args []string) int {
	path := os.Getenv("GERM_CONFIG") // the launching window's config
	if path == "" {
		path = config.Path()
	}
	cfg, _, _ := config.Load(path)
	r := &agentRun{cfg: cfg.Agents}
	if len(args) >= 2 && args[0] == "-for" {
		r.person, args = strings.TrimSpace(args[1]), args[2:]
		sh := os.Getenv("SHELL")
		if sh == "" {
			sh = "/bin/sh"
		}
		r.cfg.Launch = []string{sh}
	}
	r.task = strings.TrimSpace(strings.Join(args, " "))
	if len(r.cfg.Launch) == 0 {
		r.cfg.Launch = []string{"claude", "{task}"}
	}
	if c, err := control.Connect(); err == nil {
		r.ctl = c
		r.self = c.Self()
		defer c.Close()
	}
	if err := r.run(); err != nil {
		fmt.Fprintf(os.Stderr, "\n%sgerm agent: %v%s\n\nPress Enter to close.", red, err, reset)
		bufio.NewReader(os.Stdin).ReadString('\n')
		return 1
	}
	return 0
}

func (r *agentRun) run() error {
	if r.person != "" {
		return r.runFor()
	}
	fmt.Printf("%s◆ new agent%s  %s(%s)%s\n\n", bold+blue, reset, dim, strings.Join(r.cfg.Launch, " "), reset)
	repo, err := gitOut("", "rev-parse", "--show-toplevel")
	if err != nil {
		cwd, _ := os.Getwd()
		fmt.Printf("%s%s isn't in a git repository, so the agent will work here directly.%s\n\n", yel, cwd, reset)
	} else {
		r.repo = repo
	}
	if r.task == "" {
		fmt.Printf("%sWhat should it do?%s %s(Enter to just start it)%s\n%s›%s ", bold, reset, dim, reset, cyan, reset)
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && line == "" {
			return err
		}
		r.task = strings.TrimSpace(line)
	}
	if r.repo != "" {
		if err := r.makeWorktree(); err != nil {
			return err
		}
		r.showDiff()
	}
	r.renameTab()

	argv := expandLaunch(r.cfg.Launch, r.task)
	for {
		if err := r.runCommand(argv); err != nil {
			fmt.Printf("\n%s%v%s\n", red, err, reset)
		}
		if r.dir == "" {
			return nil // nothing to review without a worktree
		}
		next, err := r.menu()
		if err != nil || next == nil {
			r.closeDiff()
			return err
		}
		argv = next
	}
}

// runFor makes a branch and tab for a person in a shared session.
func (r *agentRun) runFor() error {
	fmt.Printf("%s◆ a tab for %s%s on its own branch\n\n", bold+blue, r.person, reset)
	repo, err := gitOut("", "rev-parse", "--show-toplevel")
	if err != nil {
		cwd, _ := os.Getwd()
		return fmt.Errorf("%s isn't in a git repository: open this from a pane in your project", cwd)
	}
	r.repo = repo
	if r.task == "" {
		fmt.Printf("%sWhat will %s work on?%s %s(a few words: it names the branch)%s\n%s›%s ", bold, r.person, reset, dim, reset, cyan, reset)
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && line == "" {
			return err
		}
		r.task = strings.TrimSpace(line)
	}
	slugTask := r.task
	if slugTask == "" {
		slugTask = r.person
	}
	r.slugFor = slug(r.person + " " + slugTask)
	if err := r.makeWorktree(); err != nil {
		return err
	}
	gitOut(r.repo, "config", "branch."+r.branch+".germ-for", r.person)
	r.showDiff()
	name := r.person
	if r.task != "" {
		name += ": " + r.task
	}
	if rs := []rune(name); len(rs) > 24 {
		name = string(rs[:23]) + "…"
	}
	if r.ctl != nil && r.self != "" {
		r.ctl.Call(control.Request{Cmd: control.CmdRenameTab, Pane: r.self, Text: "◆ " + name})
		if _, err := r.ctl.Call(control.Request{Cmd: control.CmdShareGive, Pane: r.self, Text: r.person}); err != nil {
			fmt.Printf("%scouldn't give the tab to %s: %v%s\n", yel, r.person, err, reset)
		} else {
			fmt.Printf("%s✓%s this tab is %s's now: they type here, and others ask them for the helm.\n", green, reset, r.person)
		}
	}
	fmt.Printf("%sWhen they're done, review and merge the branch with germ review (Ctrl+Shift+R).%s\n\n", dim, reset)
	err = r.runCommand(r.cfg.Launch)
	r.closeDiff()
	stat, _ := gitOut(r.dir, "diff", "--shortstat", r.base)
	fmt.Printf("\n%s◆ %s's shell exited%s  %s%s%s kept %s%s%s\n", bold+blue, r.person, reset, cyan, r.branch, reset, dim, strings.TrimSpace(stat), reset)
	fmt.Printf("%sReview, merge or discard it with germ review (Ctrl+Shift+R). Press Enter to close.%s", dim, reset)
	bufio.NewReader(os.Stdin).ReadString('\n')
	return err
}

// expandLaunch substitutes the task into the launch command.
func expandLaunch(launch []string, task string) []string {
	var out []string
	for _, a := range launch {
		if a == "{task}" {
			if task != "" {
				out = append(out, task)
			}
			continue
		}
		out = append(out, strings.ReplaceAll(a, "{task}", task))
	}
	return out
}

var slugRE = regexp.MustCompile(`[^a-z0-9]+`)

// slug makes a branch-friendly name from a task.
func slug(task string) string {
	s := strings.Trim(slugRE.ReplaceAllString(strings.ToLower(task), "-"), "-")
	words := strings.Split(s, "-")
	var out []string
	n := 0
	for _, w := range words {
		if n+len(w) > 32 || len(out) == 6 {
			break
		}
		out = append(out, w)
		n += len(w) + 1
	}
	s = strings.Join(out, "-")
	if s == "" {
		s = "agent-" + time.Now().Format("0102-1504")
	}
	return s
}

func (r *agentRun) makeWorktree() error {
	r.base, _ = gitOut(r.repo, "rev-parse", "HEAD")
	r.baseName, _ = gitOut(r.repo, "rev-parse", "--abbrev-ref", "HEAD")
	root := r.cfg.Worktrees
	if root == "" {
		root = r.repo + ".worktrees"
	} else {
		root = strings.ReplaceAll(root, "{repo}", filepath.Base(r.repo))
		if strings.HasPrefix(root, "~/") {
			home, _ := os.UserHomeDir()
			root = filepath.Join(home, root[2:])
		}
	}
	name := slug(r.task)
	if r.slugFor != "" {
		name = r.slugFor
	}
	for i := 1; ; i++ {
		cand := name
		if i > 1 {
			cand = fmt.Sprintf("%s-%d", name, i)
		}
		_, branchErr := gitOut(r.repo, "rev-parse", "--verify", "--quiet", "refs/heads/germ/"+cand)
		if _, statErr := os.Stat(filepath.Join(root, cand)); branchErr != nil && errors.Is(statErr, os.ErrNotExist) {
			name = cand
			break
		}
	}
	r.branch = "germ/" + name
	r.dir = filepath.Join(root, name)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	if _, err := gitOut(r.repo, "worktree", "add", "-b", r.branch, r.dir, r.base); err != nil {
		return fmt.Errorf("git worktree add: %v", err)
	}
	// For germ review: what the agent was asked, and where it started.
	gitOut(r.repo, "config", "branch."+r.branch+".germ-task", r.task)
	gitOut(r.repo, "config", "branch."+r.branch+".germ-base", r.base)
	gitOut(r.repo, "config", "branch."+r.branch+".germ-from", r.baseName)
	fmt.Printf("\n%s✓%s worktree %s%s%s on branch %s%s%s (from %s)\n\n", green, reset, bold, r.dir, reset,
		cyan, r.branch, reset, r.baseName)
	return os.Chdir(r.dir)
}

// showDiff opens the live diff beside the agent.
func (r *agentRun) showDiff() {
	if r.ctl == nil || r.self == "" {
		return
	}
	self, _ := os.Executable()
	cmd := fmt.Sprintf("GERM_WIDGET_BASE=%s exec %s widget git", r.base, shellQuote(self))
	resp, err := r.ctl.Call(control.Request{Cmd: control.CmdOpenPane, Pane: r.self, Where: "right",
		Size: 35, Cwd: r.dir, Command: []string{cmd}})
	if err == nil && resp.Pane != nil {
		r.diffPane = resp.Pane.Tmux
		if resp.Pane.ID != 0 {
			r.diffPane = fmt.Sprint(resp.Pane.ID)
		}
	}
}

func (r *agentRun) closeDiff() {
	if r.ctl != nil && r.diffPane != "" {
		r.ctl.Call(control.Request{Cmd: control.CmdClosePane, Pane: r.diffPane})
		r.diffPane = ""
	}
}

func (r *agentRun) renameTab() {
	if r.ctl == nil || r.self == "" {
		return
	}
	name := r.task
	if name == "" {
		name = r.cfg.Launch[0]
	}
	if rs := []rune(name); len(rs) > 24 {
		name = string(rs[:23]) + "…"
	}
	r.ctl.Call(control.Request{Cmd: control.CmdRenameTab, Pane: r.self, Text: "◆ " + name})
}

// runCommand runs argv in the foreground of this terminal, the way a
// shell would, so germ sees the agent as the pane's foreground process.
func (r *agentRun) runCommand(argv []string) error {
	if len(argv) == 0 {
		return nil
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = append(os.Environ(), "GERM_AGENT_TASK="+r.task, "GERM_AGENT_BRANCH="+r.branch)
	tty := isTTY(0)
	if tty {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Foreground: true, Ctty: 0}
	}
	// Ctrl+C goes to the agent's process group (the terminal's foreground),
	// not to us. Ignored signals would be inherited, so ignore nothing here.
	err := cmd.Run()
	if tty {
		// Take the terminal back; doing so from the background raises
		// SIGTTOU unless ignored.
		signal.Ignore(syscall.SIGTTOU)
		unix.IoctlSetPointerInt(0, unix.TIOCSPGRP, unix.Getpgrp())
		signal.Reset(syscall.SIGTTOU)
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return nil // the agent's own exit status isn't our problem
	}
	if err != nil {
		return fmt.Errorf("%s: %v", argv[0], err)
	}
	return nil
}

func isTTY(fd int) bool {
	_, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	return err == nil
}

// menu shows the worktree's state and asks what to do next. It returns a
// command to run (agent again, shell) or nil to finish.
func (r *agentRun) menu() ([]string, error) {
	for {
		dirty, _ := gitOut(r.dir, "status", "--porcelain")
		ahead, _ := gitOut(r.dir, "rev-list", "--count", r.base+"..HEAD")
		stat, _ := gitOut(r.dir, "diff", "--shortstat", r.base)
		fmt.Printf("\n%s◆ agent finished%s  %s%s%s", bold+blue, reset, cyan, r.branch, reset)
		if stat == "" && dirty == "" && (ahead == "0" || ahead == "") {
			fmt.Printf("  %sno changes%s\n", dim, reset)
		} else {
			fmt.Printf("  %s commits ahead, %s\n", ahead, strings.TrimSpace(stat))
			if dirty != "" {
				fmt.Printf("  %s%d uncommitted changes%s\n", yel, len(strings.Split(dirty, "\n")), reset)
			}
		}
		fmt.Printf("\n  %sr%s review diff     %sa%s resume agent   %ss%s shell here\n", bold, reset, bold, reset, bold, reset)
		if dirty != "" {
			fmt.Printf("  %sc%s commit all     ", bold, reset)
		} else {
			fmt.Printf("                   ")
		}
		fmt.Printf("%sm%s merge into %s, remove worktree\n", bold, reset, r.baseName)
		fmt.Printf("  %sk%s keep worktree and close    %sd%s discard everything\n\n", bold, reset, bold, reset)

		switch key := readKey(); key {
		case 'r':
			r.review()
		case 'a':
			if len(r.cfg.Resume) > 0 && r.cfg.Resume[0] == r.cfg.Launch[0] {
				return r.cfg.Resume, nil
			}
			return expandLaunch(r.cfg.Launch, ""), nil
		case 's':
			sh := os.Getenv("SHELL")
			if sh == "" {
				sh = "/bin/sh"
			}
			fmt.Printf("%sexit the shell to come back here%s\n", dim, reset)
			return []string{sh}, nil
		case 'c':
			if dirty == "" {
				continue
			}
			msg := r.task
			if msg == "" {
				msg = "Agent changes"
			}
			if _, err := gitOut(r.dir, "add", "-A"); err == nil {
				if _, err := gitOut(r.dir, "commit", "-q", "-m", msg); err != nil {
					fmt.Printf("%scommit failed: %v%s\n", red, err, reset)
				}
			}
		case 'm':
			if dirty != "" {
				fmt.Printf("%scommit or discard the uncommitted changes first (c)%s\n", yel, reset)
				continue
			}
			if err := r.merge(); err != nil {
				fmt.Printf("%smerge failed: %v%s\n", red, err, reset)
				continue
			}
			return nil, nil
		case 'k':
			fmt.Printf("kept %s (branch %s)\n", r.dir, r.branch)
			return nil, nil
		case 'd':
			fmt.Printf("%sDelete the worktree and branch %s, losing its changes? [y/N]%s ", red, r.branch, reset)
			if readKey() != 'y' {
				fmt.Println("no")
				continue
			}
			fmt.Println("yes")
			r.removeWorktree(true)
			return nil, nil
		case 3, 4: // Ctrl+C, Ctrl+D
			return nil, nil
		}
	}
}

func (r *agentRun) review() {
	pager := os.Getenv("PAGER")
	if pager == "" {
		pager = "less -R"
	}
	sh := fmt.Sprintf("{ git -c color.ui=always diff %s; git ls-files --others --exclude-standard | sed 's/^/new file: /'; } 2>&1 | %s",
		r.base, pager)
	r.runCommand([]string{"/bin/sh", "-c", sh})
}

func (r *agentRun) merge() error {
	if err := mergeBranch(r.repo, r.branch, r.baseName, r.task); err != nil {
		return err
	}
	fmt.Printf("%s✓ merged %s into %s%s\n", green, r.branch, r.baseName, reset)
	r.removeWorktree(false)
	return nil
}

func (r *agentRun) removeWorktree(force bool) {
	r.closeDiff()
	os.Chdir(r.repo)
	if err := removeWorktreeAt(r.repo, r.dir, r.branch, force); err != nil {
		fmt.Printf("%s%v%s\n", red, err, reset)
		return
	}
	fmt.Printf("removed %s\n", r.dir)
}

// mergeBranch merges an agent's branch into into (which must be checked
// out, and clean, in the main repository) with a merge commit.
func mergeBranch(repo, branch, into, task string) error {
	if dirty, _ := gitOut(repo, "status", "--porcelain", "--untracked-files=no"); dirty != "" {
		return fmt.Errorf("%s has uncommitted changes", repo)
	}
	if cur, _ := gitOut(repo, "rev-parse", "--abbrev-ref", "HEAD"); cur != into {
		return fmt.Errorf("%s is now on %s, not %s", repo, cur, into)
	}
	msg := "Merge " + branch
	if task != "" {
		msg += ": " + task
	}
	if out, err := gitOut(repo, "merge", "--no-ff", "-m", msg, branch); err != nil {
		gitOut(repo, "merge", "--abort")
		return fmt.Errorf("%v %s", err, out)
	}
	return nil
}

// removeWorktreeAt deletes an agent's worktree and branch (force: even
// with unmerged or uncommitted work).
func removeWorktreeAt(repo, dir, branch string, force bool) error {
	args := []string{"worktree", "remove", dir}
	if force {
		args = []string{"worktree", "remove", "--force", dir}
	}
	if _, err := gitOut(repo, args...); err != nil {
		return err
	}
	del := "-d"
	if force {
		del = "-D"
	}
	if _, err := gitOut(repo, "branch", del, branch); err != nil {
		return err
	}
	gitOut(repo, "config", "--remove-section", "branch."+branch)
	return nil
}

// readKey reads one key press without waiting for Enter.
func readKey() byte {
	old, err := unix.IoctlGetTermios(0, unix.TCGETS)
	if err != nil {
		var b [1]byte
		os.Stdin.Read(b[:])
		return b[0]
	}
	raw := *old
	raw.Lflag &^= unix.ICANON | unix.ECHO | unix.ISIG
	raw.Cc[unix.VMIN], raw.Cc[unix.VTIME] = 1, 0
	unix.IoctlSetTermios(0, unix.TCSETS, &raw)
	defer unix.IoctlSetTermios(0, unix.TCSETS, old)
	var b [8]byte
	n, _ := os.Stdin.Read(b[:])
	if n == 0 {
		return 4
	}
	return b[0]
}

func gitOut(dir string, args ...string) (string, error) {
	if dir != "" {
		args = append([]string{"-C", dir}, args...)
	}
	cmd := exec.Command("git", args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return strings.TrimSpace(string(out)), fmt.Errorf("%s", strings.TrimSpace(stderr.String()+" "+err.Error()))
	}
	return strings.TrimSpace(string(out)), nil
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"regexp"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/zach-monroe/germ/internal/control"
)

const ctlUsage = `usage: germ ctl COMMAND [args]

Control the germ window this shell runs in (or the newest one).

  list                          list panes
  read PANE [-n LINES]          print a pane's last lines (default 50)
  send PANE [-no-enter] TEXT    type TEXT into a pane, then Enter
  open [-where tab|right|down] [-cwd DIR] [-focus] [PANE] [-- COMMAND...]
                                open a tab or split (of PANE, default the
                                active pane), optionally running COMMAND
  wait PANE [-match REGEX] [-quiet DUR] [-timeout DUR]
                                wait until the pane shows REGEX or goes quiet
  focus PANE                    show and focus a pane
  json REQUEST                  send a raw JSON request

PANE is a germ pane ID (from list), a tmux pane ID like %3, or "self".
`

func runCtl(args []string) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "help" || args[0] == "--help" {
		fmt.Print(ctlUsage)
		return 2
	}
	c, err := control.Connect()
	if err != nil {
		fmt.Fprintln(os.Stderr, "germ ctl:", err)
		return 1
	}
	defer c.Close()
	if err := ctl(c, args[0], args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "germ ctl:", err)
		return 1
	}
	return 0
}

func paneArg(c *control.Client, s string) (string, error) {
	if s == "self" {
		if self := c.Self(); self != "" {
			return self, nil
		}
		return "", fmt.Errorf("not running inside a pane of this germ window")
	}
	return s, nil
}

func ctl(c *control.Client, cmd string, args []string) error {
	fs := flag.NewFlagSet("germ ctl "+cmd, flag.ContinueOnError)
	switch cmd {
	case "list", "ls":
		resp, err := c.Call(control.Request{Cmd: control.CmdListPanes})
		if err != nil {
			return err
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "PANE\tTMUX\tTAB\tCOMMAND\tAGENT\tSTATE\tSIZE\tCWD")
		for _, p := range resp.Panes {
			mark := ""
			if p.Active && p.Visible {
				mark = "*"
			}
			fmt.Fprintf(tw, "%d%s\t%s\t%d %s\t%s\t%s\t%s\t%dx%d\t%s\n", p.ID, mark, p.Tmux, p.Tab,
				p.TabTitle, p.Command, p.Agent, p.State, p.Cols, p.Rows, p.Cwd)
		}
		return tw.Flush()
	case "read":
		n := fs.Int("n", 50, "lines")
		pane, rest := splitPane(args)
		if err := fs.Parse(rest); err != nil {
			return err
		}
		pane, err := paneArg(c, pane)
		if err != nil {
			return err
		}
		resp, err := c.Call(control.Request{Cmd: control.CmdReadPane, Pane: pane, Lines: *n})
		if err != nil {
			return err
		}
		fmt.Println(resp.Text)
	case "send":
		noEnter := fs.Bool("no-enter", false, "don't press Enter")
		pane, rest := splitPane(args)
		if err := fs.Parse(rest); err != nil {
			return err
		}
		pane, err := paneArg(c, pane)
		if err != nil {
			return err
		}
		_, err = c.Call(control.Request{Cmd: control.CmdSendText, Pane: pane,
			Text: strings.Join(fs.Args(), " "), Enter: !*noEnter})
		return err
	case "open":
		where := fs.String("where", "tab", "tab, right, or down")
		cwd := fs.String("cwd", "", "working directory")
		focus := fs.Bool("focus", false, "focus the new pane")
		// Everything after "--" is the command (flag.Parse would eat it).
		var command []string
		for i, a := range args {
			if a == "--" {
				args, command = args[:i], args[i+1:]
				break
			}
		}
		if err := fs.Parse(args); err != nil {
			return err
		}
		req := control.Request{Cmd: control.CmdOpenPane, Where: *where, Cwd: *cwd, Focus: *focus, Command: command}
		if rest := fs.Args(); len(rest) > 0 {
			p, err := paneArg(c, rest[0])
			if err != nil {
				return err
			}
			req.Pane = p
		}
		resp, err := c.Call(req)
		if err != nil {
			return err
		}
		if resp.Pane.ID != 0 {
			fmt.Println(resp.Pane.ID)
		} else {
			fmt.Println(resp.Pane.Tmux)
		}
	case "wait":
		match := fs.String("match", "", "regular expression to wait for")
		quiet := fs.Duration("quiet", 0, "wait until no output for this long")
		timeout := fs.Duration("timeout", time.Minute, "give up after")
		pane, rest := splitPane(args)
		if err := fs.Parse(rest); err != nil {
			return err
		}
		pane, err := paneArg(c, pane)
		if err != nil {
			return err
		}
		var fn func(string) bool
		if *match != "" {
			re, err := regexp.Compile(*match)
			if err != nil {
				return err
			}
			fn = re.MatchString
		}
		if fn == nil && *quiet == 0 {
			*quiet = 2 * time.Second
		}
		r, err := c.WaitFor(pane, fn, *quiet, *timeout, 0)
		if err != nil {
			return err
		}
		if !r.Matched && !r.Quiet {
			return fmt.Errorf("timed out")
		}
	case "focus":
		if len(args) != 1 {
			return fmt.Errorf("usage: germ ctl focus PANE")
		}
		pane, err := paneArg(c, args[0])
		if err != nil {
			return err
		}
		_, err = c.Call(control.Request{Cmd: control.CmdFocusPane, Pane: pane})
		return err
	case "json":
		var req control.Request
		if err := json.Unmarshal([]byte(strings.Join(args, " ")), &req); err != nil {
			return err
		}
		resp, err := c.Call(req)
		if resp != nil {
			json.NewEncoder(os.Stdout).Encode(resp)
		}
		return err
	default:
		return fmt.Errorf("unknown command %q (see germ ctl help)", cmd)
	}
	return nil
}

// splitPane takes the leading PANE argument off args.
func splitPane(args []string) (string, []string) {
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		return args[0], args[1:]
	}
	return "", args
}

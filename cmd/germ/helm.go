package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/zach-monroe/germ/internal/control"
)

const helmUsage = `usage: germ helm [COMMAND]

The helm of a shared session's tab: whoever has it types there. Acts on
the tab this shell is in (or the window's current tab), in the germ
window this shell runs in (or the newest one).

  germ helm                   ask for it or hand it back (guests); take
                              it back (host). Same as Ctrl+Shift+O.
  germ helm status            who has the helm of each tab
  germ helm ask [TAB]         ask for it (guests)
  germ helm back [TAB]        hand it back (guests)
  germ helm give NAME [TAB]   hand NAME the helm (host)
  germ helm take [TAB|all]    take it back (host): this tab, or if no
                              guest has it, every tab one does

TAB is a tab's number, as the tab bar shows it.
`

func runHelm(args []string) int {
	if len(args) > 0 && (args[0] == "-h" || args[0] == "help" || args[0] == "--help") {
		fmt.Print(helmUsage)
		return 2
	}
	c, err := control.Connect()
	if err != nil {
		fmt.Fprintln(os.Stderr, "germ helm:", err)
		return 1
	}
	defer c.Close()
	resp, err := c.Call(control.Request{Cmd: control.CmdHelm, Pane: c.Self(), Text: strings.Join(args, " ")})
	if err != nil {
		fmt.Fprintln(os.Stderr, "germ helm:", err)
		return 1
	}
	if resp.Text != "" {
		fmt.Println(resp.Text)
	}
	return 0
}

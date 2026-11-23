// Command germ-vtdump runs a program (or replays a recording) through germ's
// terminal emulation without a window and prints the resulting screen.
//
//	germ-vtdump -cols 80 -rows 24 -wait 2s -- htop
//	germ-vtdump -keys ':q\r' -- vim file.go
//	germ-vtdump -replay session.raw
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/zach-monroe/germ/internal/pty"
	"github.com/zach-monroe/germ/internal/term"
)

func main() {
	cols := flag.Int("cols", 80, "columns")
	rows := flag.Int("rows", 24, "rows")
	wait := flag.Duration("wait", 1500*time.Millisecond, "how long to let the program run before dumping (and again after -keys)")
	keys := flag.String("keys", "", `input to send after -wait, Go string escapes allowed (e.g. "jj:q\r")`)
	replay := flag.String("replay", "", "feed raw bytes from this file instead of running a program")
	scrollback := flag.Bool("scrollback", false, "also print scrollback")
	flag.Parse()

	tm, err := run(*cols, *rows, *wait, *keys, *replay, flag.Args())
	if err != nil {
		fmt.Fprintln(os.Stderr, "germ-vtdump:", err)
		os.Exit(1)
	}
	dump(os.Stdout, tm, *cols, *scrollback)
}

func run(cols, rows int, wait time.Duration, keys, replay string, argv []string) (*term.Terminal, error) {
	if replay != "" {
		data, err := os.ReadFile(replay)
		if err != nil {
			return nil, err
		}
		tm := term.New(cols, rows, term.Options{Scrollback: 10000})
		tm.Write(data)
		return tm, nil
	}
	if len(argv) == 0 {
		return nil, fmt.Errorf("no command given (use -- cmd args, or -replay)")
	}
	sess, err := pty.Start(argv, uint16(cols), uint16(rows))
	if err != nil {
		return nil, err
	}
	defer sess.Close()
	tm := term.New(cols, rows, term.Options{Scrollback: 10000, Responder: sess})
	go io.Copy(tm, sess)

	time.Sleep(wait)
	if keys != "" {
		k, err := strconv.Unquote(`"` + strings.ReplaceAll(keys, `"`, `\"`) + `"`)
		if err != nil {
			return nil, fmt.Errorf("bad -keys: %w", err)
		}
		sess.Write([]byte(k))
		time.Sleep(wait)
	}
	return tm, nil
}

func dump(w io.Writer, tm *term.Terminal, cols int, withScrollback bool) {
	if withScrollback {
		if sb := tm.ScrollbackText(); sb != "" {
			fmt.Fprintln(w, "── scrollback ──")
			fmt.Fprintln(w, sb)
		}
	}
	text := tm.Text()
	tm.Lock()
	c, alt, title, modes := tm.Cursor(), tm.AltScreen(), tm.Title(), tm.Modes()
	tm.Unlock()

	border := "+" + strings.Repeat("-", cols) + "+"
	fmt.Fprintln(w, border)
	for _, line := range strings.Split(text, "\n") {
		pad := cols - displayWidth(line)
		fmt.Fprintf(w, "|%s%s|\n", line, strings.Repeat(" ", max(pad, 0)))
	}
	fmt.Fprintln(w, border)
	fmt.Fprintf(w, "cursor=(%d,%d) visible=%v alt=%v title=%q\n", c.X, c.Y, modes.CursorVisible, alt, title)
}

// displayWidth approximates column width: wide runes count as 2.
func displayWidth(s string) int {
	n := 0
	for _, r := range s {
		if r >= 0x1100 && (r <= 0x115f || (r >= 0x2e80 && r <= 0xa4cf) || (r >= 0xac00 && r <= 0xd7a3) ||
			(r >= 0xf900 && r <= 0xfaff) || (r >= 0xfe30 && r <= 0xfe4f) || (r >= 0xff00 && r <= 0xff60) ||
			(r >= 0x1f300 && r <= 0x1faff) || (r >= 0x20000 && r <= 0x3fffd)) {
			n += 2
		} else {
			n++
		}
	}
	return n
}

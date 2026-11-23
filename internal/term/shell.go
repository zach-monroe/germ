package term

import (
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Shell integration: OSC 7 reports the working directory and OSC 133
// ("FinalTerm" semantic prompts) marks prompt, command, and output
// boundaries. germ's injected fish script emits both.

type absPos struct {
	row int64
	col int
}

type shellMarks struct {
	outputStart, outputEnd absPos
	haveOutput             bool // outputStart..outputEnd is a finished command's output
	inOutput               bool // between C and D
	cmdStart               time.Time
}

func (s *shellMarks) invalidate() { s.haveOutput, s.inOutput = false, false }

// CWD returns the shell's working directory from OSC 7, or "". Caller must
// hold the lock.
func (t *Terminal) CWD() string { return t.cwd }

// LastOutput returns the absolute range of the most recent finished
// command's output. Caller must hold the lock.
func (t *Terminal) LastOutput() (startRow int64, startCol int, endRow int64, endCol int, ok bool) {
	s := &t.shell
	if !s.haveOutput {
		return 0, 0, 0, 0, false
	}
	return s.outputStart.row, s.outputStart.col, s.outputEnd.row, s.outputEnd.col, true
}

func (t *Terminal) cursorAbs() absPos {
	return absPos{row: t.pushed + int64(t.cur.Y), col: t.cur.X}
}

func (t *Terminal) oscCWD(rest string) {
	u, err := url.Parse(rest)
	if err != nil || u.Scheme != "file" {
		return
	}
	t.cwd = u.Path
}

func (t *Terminal) oscSemanticPrompt(rest string) {
	if t.active != &t.primary || rest == "" {
		return // marks only make sense on the primary screen
	}
	parts := strings.Split(rest, ";")
	switch parts[0] {
	case "A": // prompt start
		t.active.lines[t.cur.Y].Prompt = true
		t.markDirty(t.cur.Y)
	case "B": // prompt end / command input start
	case "C": // command output start
		t.shell.outputStart = t.cursorAbs()
		t.shell.inOutput = true
		t.shell.cmdStart = time.Now()
	case "D": // command finished, optional exit status
		if !t.shell.inOutput {
			return // D without C: e.g. an empty command line
		}
		t.shell.inOutput = false
		end := t.cursorAbs()
		// The output ends just before the cursor (normally at column 0
		// of the line after the last output line).
		if end.col == 0 && end.row > t.shell.outputStart.row {
			end.row--
			end.col = t.cols - 1
		} else {
			end.col = max(0, end.col-1)
		}
		t.shell.outputEnd = end
		t.shell.haveOutput = !end.before(t.shell.outputStart)
		exit := 0
		if len(parts) > 1 {
			exit, _ = strconv.Atoi(parts[1])
		}
		// Record the status on the prompt line that started this command.
		for row := t.shell.outputStart.row; row >= t.shell.outputStart.row-200; row-- {
			l := t.LineAt(row)
			if l == nil {
				break
			}
			if l.Prompt {
				l.HasExit, l.Exit = true, int32(exit)
				if rel := row - t.pushed; rel >= 0 && rel < int64(t.rows) {
					t.markDirty(int(rel))
				}
				break
			}
		}
		if t.opts.OnCommandDone != nil {
			t.opts.OnCommandDone(exit, time.Since(t.shell.cmdStart))
		}
	}
}

func (p absPos) before(q absPos) bool {
	return p.row < q.row || (p.row == q.row && p.col < q.col)
}

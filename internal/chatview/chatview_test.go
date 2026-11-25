package chatview

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/zach-monroe/germ/internal/share"
	"github.com/zach-monroe/germ/internal/term"
)

func TestWrapText(t *testing.T) {
	for _, c := range []struct {
		in    string
		width int
		want  string
	}{
		{"hello world", 20, "hello world"},
		{"hello world", 8, "hello|world"},
		{"a verylongwordthatdoesntfit", 10, "a|verylongwo|rdthatdoes|ntfit"},
		{"two\nlines", 20, "two|lines"},
		{"日本語のテキスト", 6, "日本語|のテキ|スト"}, // double width
		{"bad\x1b[31m", 20, "bad [31m"},
	} {
		if got := strings.Join(WrapText(c.in, c.width), "|"); got != c.want {
			t.Errorf("WrapText(%q, %d) = %q, want %q", c.in, c.width, got, c.want)
		}
	}
}

func TestWrapInputCursor(t *testing.T) {
	for _, c := range []struct {
		in       string
		cur      int
		lines    string
		row, col int
	}{
		{"", 0, "", 0, 0},
		{"abc", 3, "abc", 0, 3},
		{"abc", 1, "abc", 0, 1},
		{"abcdef", 6, "abcd|ef", 1, 2},
		{"abcd", 4, "abcd|", 1, 0}, // a full line: the cursor wraps
		{"ab\ncd", 3, "ab|cd", 1, 0},
	} {
		lines, row, col := WrapInput([]rune(c.in), c.cur, 4)
		if strings.Join(lines, "|") != c.lines || row != c.row || col != c.col {
			t.Errorf("WrapInput(%q, %d) = %q %d,%d; want %q %d,%d", c.in, c.cur, strings.Join(lines, "|"), row, col, c.lines, c.row, c.col)
		}
	}
}

// screen renders the panel into a terminal and returns its rows as text.
func screen(v View, cols, rows int) []string {
	out, _ := Render(v, cols, rows)
	tm := term.New(cols, rows, term.Options{})
	tm.Write([]byte(out))
	return strings.Split(tm.Text(), "\n")
}

func TestRenderChat(t *testing.T) {
	at := time.Date(2026, 11, 25, 14, 2, 0, 0, time.Local).UnixMilli()
	v := View{
		People: []share.Participant{{Name: "zach@box", Host: true}, {ID: 1, Name: "alice@lap"}},
		Msgs: []share.ChatMsg{
			{Seq: 1, Event: true, Text: "alice joined", At: at},
			{Seq: 2, From: 1, Name: "alice@lap", Text: "pushed the fix", At: at},
			{Seq: 3, From: 1, Name: "alice@lap", Text: "tests pass", At: at},
			{Seq: 4, Name: "zach@box", Host: true, Text: "on it @alice", At: at + 60000},
		},
		You: 1, Me: "alice", Decision: "bob wants the helm of tab 2 (zsh)", Typing: []string{"zach"},
		Focused: true,
	}
	got := screen(v, 30, 16)
	text := strings.Join(got, "\n")
	for _, want := range []string{
		"chat · 2 here zach★ alice",
		"· alice joined",
		"alice · 14:02",
		"  pushed the fix",
		"  tests pass",
		"zach ★ · 14:03",
		"on it @alice",
		"» bob wants the helm of tab",
		"y yes · n no",
		"zach is typing…",
		"› message · Enter sends",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
	// One header for alice's two messages in a row.
	if strings.Count(text, "alice · 14:02") != 1 {
		t.Errorf("headers:\n%s", text)
	}
}

func TestRenderChatScrollsAndDivider(t *testing.T) {
	var msgs []share.ChatMsg
	for i := 1; i <= 30; i++ {
		msgs = append(msgs, share.ChatMsg{Seq: uint64(i), From: 1, Name: "alice", Text: "message " + strconv.Itoa(i)})
	}
	v := View{Msgs: msgs, Divider: 28, Input: []rune("hi")}
	got := strings.Join(screen(v, 30, 12), "\n")
	if !strings.Contains(got, "message 30") || strings.Contains(got, "message 10") {
		t.Errorf("not showing the newest:\n%s", got)
	}
	if !strings.Contains(got, " new ") {
		t.Errorf("no Divider:\n%s", got)
	}
	v.Scroll = 5
	got = strings.Join(screen(v, 30, 12), "\n")
	if strings.Contains(got, "message 30") || !strings.Contains(got, "more · PgDn") {
		t.Errorf("scrolled:\n%s", got)
	}
}

func TestMentions(t *testing.T) {
	if !mentions("hey @Alice look", "alice") || !mentions("@here standup", "bob") || mentions("alice: hi", "alice") {
		t.Error("mentions")
	}
}

// The share pane: its own header rows, a notice, and a request on one row
// when there's room.
func TestRenderSharePane(t *testing.T) {
	v := View{
		Head:     []string{"◆ contagious · 1 alice", "link https://x/j/abc"},
		Decision: "bob wants to join", Focused: true, Notice: "copied the invite link",
		Hint: "message, or /help",
		Msgs: []share.ChatMsg{{Seq: 1, Event: true, Text: "alice joined"}},
	}
	text := strings.Join(screen(v, 70, 10), "\n")
	for _, want := range []string{"◆ contagious · 1 alice", "link https://x/j/abc", "· alice joined",
		"» bob wants to join  y yes · n no", "copied the invite link", "› message, or /help"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
	if strings.Contains(text, "chat · ") {
		t.Errorf("default header drawn too:\n%s", text)
	}
}

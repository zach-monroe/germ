package chatview

import (
	"strings"
	"unicode"
)

// MaxInput is the most runes a message box holds.
const MaxInput = 4000

// Input is a chat message box: its text, the cursor, and the messages
// sent from it (Up and Down recall them). Germ's chat panel and the share
// pane map their keys onto it.
type Input struct {
	Text   []rune
	Cur    int
	sent   []string
	sentAt int
}

// Insert adds typed or pasted text at the cursor (tabs become spaces,
// other control characters but newlines are dropped). It reports whether
// anything was added.
func (in *Input) Insert(text string) bool {
	var rs []rune
	for _, r := range strings.ReplaceAll(text, "\r\n", "\n") {
		if r == '\t' || r == '\r' {
			r = ' '
		}
		if r == '\n' || !unicode.IsControl(r) {
			rs = append(rs, r)
		}
	}
	if len(rs) == 0 || len(in.Text)+len(rs) > MaxInput {
		return false
	}
	in.Text = append(in.Text[:in.Cur], append(rs, in.Text[in.Cur:]...)...)
	in.Cur += len(rs)
	return true
}

// Backspace deletes the rune before the cursor.
func (in *Input) Backspace() {
	if in.Cur > 0 {
		in.Text = append(in.Text[:in.Cur-1], in.Text[in.Cur:]...)
		in.Cur--
	}
}

// Delete deletes the rune at the cursor.
func (in *Input) Delete() {
	if in.Cur < len(in.Text) {
		in.Text = append(in.Text[:in.Cur], in.Text[in.Cur+1:]...)
	}
}

// DeleteWord deletes the word before the cursor (Ctrl+W).
func (in *Input) DeleteWord() {
	i := in.Cur
	for i > 0 && unicode.IsSpace(in.Text[i-1]) {
		i--
	}
	for i > 0 && !unicode.IsSpace(in.Text[i-1]) {
		i--
	}
	in.Text, in.Cur = append(in.Text[:i], in.Text[in.Cur:]...), i
}

// KillToStart deletes everything before the cursor (Ctrl+U).
func (in *Input) KillToStart() { in.Text, in.Cur = in.Text[in.Cur:], 0 }

// KillToEnd deletes everything after the cursor (Ctrl+K).
func (in *Input) KillToEnd() { in.Text = in.Text[:in.Cur] }

func (in *Input) Left()  { in.Cur = max(0, in.Cur-1) }
func (in *Input) Right() { in.Cur = min(len(in.Text), in.Cur+1) }
func (in *Input) Home()  { in.Cur = 0 }
func (in *Input) End()   { in.Cur = len(in.Text) }

// Clear empties the box.
func (in *Input) Clear() { in.Text, in.Cur = nil, 0 }

// Empty reports whether the box is empty.
func (in *Input) Empty() bool { return len(in.Text) == 0 }

// CanRecall reports whether Up recalls a sent message: the box is empty or
// already showing one.
func (in *Input) CanRecall() bool {
	return in.sentAt > 0 && (len(in.Text) == 0 || in.sentAt < len(in.sent))
}

// Up recalls the previous sent message.
func (in *Input) Up() {
	if in.CanRecall() {
		in.sentAt--
		in.Text = []rune(in.sent[in.sentAt])
		in.Cur = len(in.Text)
	}
}

// Recalling reports whether the box shows a recalled message (so Down
// moves through them).
func (in *Input) Recalling() bool { return in.sentAt < len(in.sent) }

// Down moves to the next sent message, or back to an empty box.
func (in *Input) Down() {
	if !in.Recalling() {
		return
	}
	in.sentAt++
	in.Text = nil
	if in.sentAt < len(in.sent) {
		in.Text = []rune(in.sent[in.sentAt])
	}
	in.Cur = len(in.Text)
}

// Take empties the box and returns its text, trimmed, remembering it for
// Up ("" if it was blank, which is left as it is).
func (in *Input) Take() string {
	text := strings.TrimSpace(string(in.Text))
	if text == "" {
		return ""
	}
	in.Clear()
	in.sent = append(in.sent, text)
	in.sentAt = len(in.sent)
	return text
}

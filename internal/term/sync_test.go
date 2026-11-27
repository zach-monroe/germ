package term

import (
	"strings"
	"testing"
	"time"
)

const bsu, esu = "\x1b[?2026h", "\x1b[?2026l"

func TestSyncHoldsUntilEnd(t *testing.T) {
	tm := New(10, 1, Options{})
	tm.Write([]byte(bsu + "half"))
	if got := tm.Text(); got != "" {
		t.Fatalf("mid-update screen = %q, want nothing yet", got)
	}
	tm.Write([]byte(" done" + esu))
	if got := tm.Text(); got != "half done" {
		t.Fatalf("after end = %q", got)
	}
}

// The read that ends one frame also begins the next (common under tmux):
// the finished frame must show, not wait for the next one.
func TestSyncFrameEndAndNextBegin(t *testing.T) {
	tm := New(10, 1, Options{})
	tm.Write([]byte(bsu + "one" + esu + bsu + "\rtw"))
	if got := tm.Text(); got != "one" {
		t.Fatalf("screen = %q, want the finished frame", got)
	}
	tm.Write([]byte("o" + esu))
	if got := tm.Text(); got != "two" {
		t.Fatalf("screen = %q", got)
	}
}

func TestSyncMarkersSplitAcrossWrites(t *testing.T) {
	tm := New(10, 1, Options{})
	in := bsu + "abc" + esu + "d"
	for i := 0; i < len(in); i++ { // one byte at a time
		tm.Write([]byte{in[i]})
		if got := tm.Text(); i < len(in)-1 && strings.Contains(got, "c") && !strings.HasPrefix(got, "abc") {
			t.Fatalf("partial frame shown: %q", got)
		}
	}
	if got := tm.Text(); got != "abcd" {
		t.Fatalf("screen = %q", got)
	}
	tm.Lock()
	held := !tm.syncSince.IsZero()
	tm.Unlock()
	if held {
		t.Fatal("still holding after the end")
	}
}

func TestSyncTimeout(t *testing.T) {
	tm := New(10, 1, Options{})
	tm.Write([]byte(bsu + "stuck"))
	tm.Lock()
	if left := tm.SyncWait(); left <= 0 || left > SyncTimeout {
		t.Errorf("SyncWait = %v", left)
	}
	tm.syncSince = time.Now().Add(-SyncTimeout - time.Millisecond)
	left := tm.SyncWait()
	sync := tm.modes.SyncOutput
	tm.Unlock()
	if left != 0 || sync {
		t.Errorf("after timeout: left %v, mode still on %v", left, sync)
	}
	if got := tm.Text(); got != "stuck" {
		t.Errorf("screen = %q", got)
	}
	tm.Write([]byte("!")) // no longer held
	if got := tm.Text(); got != "stuck!" {
		t.Errorf("screen = %q", got)
	}
}

func TestSyncModeQuery(t *testing.T) {
	tm, resp := newTerm(10, 1)
	tm.Write([]byte(bsu + "\x1b[?2026$p" + esu + "\x1b[?2026$p"))
	if got := resp.String(); got != "\x1b[?2026;1$y\x1b[?2026;2$y" {
		t.Errorf("DECRQM replies = %q", got)
	}
}

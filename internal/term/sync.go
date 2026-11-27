package term

import (
	"bytes"
	"time"
)

// Synchronized updates (mode 2026). Between "begin" (CSI ? 2026 h) and
// "end" (CSI ? 2026 l) the bytes are held unparsed, then parsed in one go
// when the end arrives, so the grid only ever holds complete frames and
// the renderer can draw whenever it likes. (Gating the renderer on the mode
// instead hides a finished frame whenever the read that ends it also
// begins the next one, which is common under tmux, whose %output chunks
// don't follow frames.)

// SyncTimeout bounds how long an update may be held, in case an app never
// ends it.
const SyncTimeout = 200 * time.Millisecond

const syncMax = 4 << 20 // held bytes past which the update is applied anyway

var (
	syncBegin = []byte("\x1b[?2026h")
	syncEnd   = []byte("\x1b[?2026l")
)

// advance feeds output to the parser, holding synchronized updates.
// Caller holds the lock.
func (t *Terminal) advance(p []byte) {
	for len(p) > 0 {
		if t.syncSince.IsZero() {
			i := bytes.Index(p, syncBegin)
			if i < 0 {
				t.parser.Advance(p)
				// A begin split across reads (or with other modes in the
				// same sequence) is noticed here, and the rest is held.
				if t.modes.SyncOutput {
					t.syncSince = time.Now()
				}
				return
			}
			i += len(syncBegin)
			t.parser.Advance(p[:i])
			p = p[i:]
			if t.modes.SyncOutput {
				t.syncSince = time.Now()
			}
			continue
		}
		from := max(0, len(t.syncBuf)-len(syncEnd)+1) // an end split across reads
		t.syncBuf = append(t.syncBuf, p...)
		p = nil
		j := bytes.Index(t.syncBuf[from:], syncEnd)
		if j < 0 {
			if len(t.syncBuf) > syncMax {
				t.flushSync()
			}
			return
		}
		end := from + j + len(syncEnd)
		held, rest := t.syncBuf[:end], t.syncBuf[end:]
		t.syncSince = time.Time{}
		t.parser.Advance(held)
		if len(rest) > 0 {
			p = append([]byte(nil), rest...)
		}
		t.syncBuf = t.syncBuf[:0]
	}
}

// flushSync applies a held update now. Caller holds the lock.
func (t *Terminal) flushSync() {
	held := t.syncBuf
	t.syncBuf, t.syncSince = nil, time.Time{}
	t.parser.Advance(held)
	if t.modes.SyncOutput { // never ended: stop holding until the next begin
		t.modes.SyncOutput = false
	}
}

// SyncWait applies a synchronized update held for longer than SyncTimeout
// and returns how long until the one being held times out, or 0 if none
// is. Caller holds the lock.
func (t *Terminal) SyncWait() time.Duration {
	if t.syncSince.IsZero() {
		return 0
	}
	left := SyncTimeout - time.Since(t.syncSince)
	if left <= 0 {
		t.flushSync()
		return 0
	}
	return left
}

package term

import (
	"strconv"
	"strings"
)

// Desktop notifications and progress, as emitted by agents and long-running
// tools:
//
//	OSC 9 ; body ST                      (iTerm2)
//	OSC 9 ; 4 ; state ; percent ST       (ConEmu/Windows Terminal progress)
//	OSC 777 ; notify ; title ; body ST   (rxvt/foot/Ghostty)
//	OSC 99 ; key=val:... ; payload ST    (kitty; chunked with d=0)

type kittyNotify struct {
	id          string
	title, body string
}

func (t *Terminal) oscNotify(code int, rest string) {
	switch code {
	case 9:
		if p, ok := strings.CutPrefix(rest, "4;"); ok {
			t.oscProgress(p)
			return
		}
		t.notify("", rest)
	case 777:
		f := strings.SplitN(rest, ";", 3)
		if len(f) >= 2 && f[0] == "notify" {
			body := ""
			if len(f) == 3 {
				body = f[2]
			}
			t.notify(f[1], body)
		}
	case 99:
		meta, payload, _ := strings.Cut(rest, ";")
		done, part, id := true, "title", ""
		encoded := false
		for _, kv := range strings.Split(meta, ":") {
			k, v, _ := strings.Cut(kv, "=")
			switch k {
			case "d":
				done = v != "0"
			case "p":
				part = v
			case "i":
				id = v
			case "e":
				encoded = v == "1"
			}
		}
		if encoded || (part != "title" && part != "body") {
			return // base64 payloads and queries/icons aren't worth it here
		}
		n := &t.kitty
		if n.id != id {
			*n = kittyNotify{id: id}
		}
		if part == "title" {
			n.title += payload
		} else {
			n.body += payload
		}
		if done {
			t.notify(n.title, n.body)
			*n = kittyNotify{}
		}
	}
}

func (t *Terminal) notify(title, body string) {
	if t.opts.OnNotify != nil {
		t.opts.OnNotify(sanitize(title), sanitize(body))
	}
}

func (t *Terminal) oscProgress(s string) {
	f := strings.Split(s, ";")
	state, _ := strconv.Atoi(f[0])
	pct := 0
	if len(f) > 1 {
		pct, _ = strconv.Atoi(f[1])
	}
	if t.opts.OnProgress != nil {
		t.opts.OnProgress(state, max(0, min(pct, 100)))
	}
}

// sanitize drops control characters from notification text.
func sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
}

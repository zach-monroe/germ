package term

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	"github.com/zach-monroe/germ/internal/vt"
)

// OSC 52 clipboard, DECRQSS, XTGETTCAP, and xterm modifyOtherKeys.

// maxClipboard bounds OSC 52 payloads (decoded).
const maxClipboard = 1 << 20

// oscClipboard handles OSC 52 ; selection ; base64. Applications may set
// the clipboard (this is how tmux's copy mode and remote vim reach your
// clipboard). Reading it back is refused: that would let any program,
// including one on a remote host, read your clipboard.
func (t *Terminal) oscClipboard(rest string) {
	sel, data, ok := strings.Cut(rest, ";")
	if !ok || data == "?" || t.opts.OnClipboard == nil {
		return
	}
	if sel == "" {
		sel = "s0"
	}
	text, err := base64.StdEncoding.DecodeString(data)
	if err != nil || len(text) > maxClipboard {
		return
	}
	primary := strings.ContainsAny(sel, "ps")
	clipboard := strings.ContainsAny(sel, "c0") || !primary
	t.opts.OnClipboard(string(text), clipboard, primary)
}

func (h *handler) dcs(s *vt.Seq, final byte, data []byte) {
	t := h.t()
	switch {
	case s.Intermediate() == '$' && final == 'q': // DECRQSS
		t.respondDECRQSS(string(data))
	case s.Intermediate() == '+' && final == 'q': // XTGETTCAP
		t.respondXTGETTCAP(string(data))
	}
}

// respondDECRQSS reports the current setting for a control function.
func (t *Terminal) respondDECRQSS(q string) {
	var v string
	switch q {
	case "m":
		v = sgrString(t.cur.Pen) + "m"
	case "r":
		v = fmt.Sprintf("%d;%dr", t.top+1, t.bottom+1)
	case " q":
		v = fmt.Sprintf("%d q", t.cursorStyle)
	case "\"q": // DECSCA: protection is not supported
		v = "0\"q"
	case "\"p": // DECSCL: conformance level (VT220, 7-bit)
		v = "62;1\"p"
	default:
		t.respond("\x1bP0$r\x1b\\")
		return
	}
	t.respond("\x1bP1$r" + v + "\x1b\\")
}

// sgrString renders the pen as SGR parameters.
func sgrString(c Cell) string {
	p := []string{"0"}
	for _, a := range []struct {
		bit Attr
		n   string
	}{{AttrBold, "1"}, {AttrFaint, "2"}, {AttrItalic, "3"}, {AttrBlink, "5"},
		{AttrInverse, "7"}, {AttrHidden, "8"}, {AttrStrike, "9"}, {AttrOverline, "53"}} {
		if c.Attrs&a.bit != 0 {
			p = append(p, a.n)
		}
	}
	switch c.Underline {
	case UnderlineNone:
	case UnderlineSingle:
		p = append(p, "4")
	default:
		p = append(p, "4:"+strconv.Itoa(int(c.Underline)))
	}
	color := func(c Color, base, ext int) {
		if i, ok := c.Palette(); ok {
			switch {
			case i < 8 && base > 0:
				p = append(p, strconv.Itoa(base+int(i)))
			case i < 16 && base > 0:
				p = append(p, strconv.Itoa(base+60+int(i)-8))
			default:
				p = append(p, fmt.Sprintf("%d:5:%d", ext, i))
			}
		} else if r, g, b, ok := c.RGB(); ok {
			p = append(p, fmt.Sprintf("%d:2::%d:%d:%d", ext, r, g, b))
		}
	}
	color(c.FG, 30, 38)
	color(c.BG, 40, 48)
	color(c.UL, 0, 58)
	return strings.Join(p, ";")
}

// termcaps answers XTGETTCAP queries, mirroring the terminfo entry.
var termcaps = map[string]string{
	"TN":     "germ",
	"name":   "germ",
	"Co":     "256",
	"colors": "256",
	"RGB":    "8/8/8",
	"Tc":     "",
	"Smulx":  `\E[4:%p1%dm`,
	"Setulc": `\E[58:2::%p1%{65536}%/%d:%p1%{256}%/%{255}%&%d:%p1%{255}%&%d%;m`,
	"Ss":     `\E[%p1%d q`,
	"Se":     `\E[2 q`,
	"Sync":   `\E[?2026%?%p1%{1}%-%tl%eh%;`,
	"Ms":     `\E]52;%p1%s;%p2%s\007`,
	"Smol":   `\E[53m`,
	"Rmol":   `\E[55m`,
}

func (t *Terminal) respondXTGETTCAP(data string) {
	for _, h := range strings.Split(data, ";") {
		nameB, err := hex.DecodeString(h)
		if err != nil {
			continue
		}
		name := string(nameB)
		v, ok := termcaps[name]
		if !ok {
			t.respond("\x1bP0+r" + h + "\x1b\\")
			continue
		}
		reply := h
		if v != "" {
			reply += "=" + strings.ToUpper(hex.EncodeToString([]byte(v)))
		}
		t.respond("\x1bP1+r" + reply + "\x1b\\")
	}
}

// ModifyOtherKeys returns the xterm modifyOtherKeys level (0–2) set with
// CSI > 4 ; n m. Caller must hold the lock.
func (t *Terminal) ModifyOtherKeys() int { return t.modifyOtherKeys }

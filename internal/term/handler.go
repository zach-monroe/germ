package term

import (
	"fmt"
	"strconv"

	"github.com/mattn/go-runewidth"

	"github.com/zach-monroe/germ/internal/vt"
)

// Version is reported by XTVERSION.
const Version = "0.1.0"

// Kitty keyboard protocol flags.
const (
	KittyDisambiguate  = 1
	KittyEventTypes    = 2
	KittyAlternateKeys = 4
	KittyAllKeys       = 8
	KittyText          = 16

	// KittySupported is the subset germ implements; others are masked off
	// so the query reply is honest.
	KittySupported = KittyDisambiguate | KittyEventTypes | KittyAllKeys

	maxKbdStack = 16
)

// handler adapts Terminal to vt.Handler without exporting the methods on
// Terminal itself. The parser runs with t.mu held.
type handler Terminal

func (h *handler) t() *Terminal { return (*Terminal)(h) }

func (h *handler) Print(r rune)        { h.t().print(r) }
func (h *handler) PrintASCII(b []byte) { h.t().printASCII(b) }

var widthCond = &runewidth.Condition{EastAsianWidth: false}

func runeWidth(r rune) int {
	if r < 0x300 {
		return 1 // fast path: Latin, no combining marks below U+0300
	}
	return widthCond.RuneWidth(r)
}

func (h *handler) Execute(c byte) {
	t := h.t()
	switch c {
	case 0x07: // BEL
		if t.opts.OnBell != nil {
			t.opts.OnBell()
		}
	case 0x08: // BS
		if t.cur.X > 0 {
			t.cur.X--
		}
		t.cur.pendingWrap = false
	case 0x09: // HT
		t.tabForward(1)
	case 0x0a, 0x0b, 0x0c: // LF, VT, FF
		t.index()
		if t.modes.LineFeedNewLine {
			t.carriageReturn()
		}
	case 0x0d: // CR
		t.carriageReturn()
	case 0x0e: // SO: invoke G1
		t.cur.gl = 1
	case 0x0f: // SI: invoke G0
		t.cur.gl = 0
	}
}

func (h *handler) EscDispatch(s *vt.Seq, final byte) {
	t := h.t()
	switch s.Intermediate() {
	case 0:
		switch final {
		case '7':
			t.saveCursor()
		case '8':
			t.restoreCursor()
		case 'D':
			t.index()
		case 'E':
			t.carriageReturn()
			t.index()
		case 'H':
			t.tabs[t.cur.X] = true
		case 'M':
			t.reverseIndex()
		case 'c':
			t.fullReset()
		case '=':
			t.modes.AppKeypad = true
		case '>':
			t.modes.AppKeypad = false
		}
	case '(', ')':
		slot := 0
		if s.Intermediate() == ')' {
			slot = 1
		}
		switch final {
		case '0':
			t.cur.charsets[slot] = charsetDECGraphics
		case 'B':
			t.cur.charsets[slot] = charsetASCII
		}
	case '#':
		if final == '8' { // DECALN: fill screen with 'E'
			for y := range t.active.lines {
				l := &t.active.lines[y]
				for x := range l.Cells {
					l.Cells[x] = Cell{Rune: 'E'}
				}
				l.touch(len(l.Cells))
				l.Wrapped = false
			}
			t.top, t.bottom = 0, t.rows-1
			t.moveTo(0, 0)
			t.markAllDirty()
		}
	}
}

func (h *handler) CsiDispatch(s *vt.Seq, final byte) {
	t := h.t()
	p := &s.Params
	n := int(p.Get(0, 1)) // most sequences: count defaulting to 1

	switch s.Prefix {
	case '?':
		h.csiPrivate(s, final)
		return
	case '>':
		switch final {
		case 'c': // DA2: VT220-ish, version 100
			t.respond("\x1b[>1;100;0c")
		case 'q': // XTVERSION
			if p.Raw(0) == 0 {
				t.respond("\x1bP>|germ " + Version + "\x1b\\")
			}
		case 'm': // XTMODKEYS: CSI > 4 ; level m (modifyOtherKeys)
			if p.Raw(0) == 4 {
				t.modifyOtherKeys = int(min(p.Raw(1), 2))
			}
		case 'n': // XTMODKEYS reset
			if p.Raw(0) == 4 {
				t.modifyOtherKeys = 0
			}
		case 'u': // kitty keyboard: push flags
			k := &t.active.kbd
			if len(*k) >= maxKbdStack {
				*k = (*k)[1:]
			}
			*k = append(*k, uint8(p.Raw(0))&KittySupported)
		}
		return
	case '<':
		if final == 'u' { // kitty keyboard: pop n entries
			k := &t.active.kbd
			*k = (*k)[:max(0, len(*k)-int(p.Get(0, 1)))]
		}
		return
	case '=':
		if final == 'u' { // kitty keyboard: set flags (mode 1 set, 2 or, 3 and-not)
			k := &t.active.kbd
			if len(*k) == 0 {
				*k = append(*k, 0)
			}
			f, top := uint8(p.Raw(0))&KittySupported, &(*k)[len(*k)-1]
			switch p.Get(1, 1) {
			case 1:
				*top = f
			case 2:
				*top |= f
			case 3:
				*top &^= f
			}
		}
		return
	case 0:
	default:
		return
	}

	switch s.Intermediate() {
	case 0:
	case ' ':
		if final == 'q' { // DECSCUSR
			if v := p.Raw(0); v <= uint16(CursorSteadyBar) {
				t.cursorStyle = CursorStyle(v)
			}
		}
		return
	case '!':
		if final == 'p' {
			t.softReset()
		}
		return
	case '$':
		if final == 'p' { // DECRQM (ANSI modes)
			h.reportMode(int(p.Raw(0)), false)
		}
		return
	default:
		return
	}

	c := &t.cur
	switch final {
	case '@': // ICH
		t.insertCells(n)
	case 'A': // CUU
		top := 0
		if c.Y >= t.top {
			top = t.top
		}
		c.Y = max(c.Y-n, top)
		c.pendingWrap = false
	case 'B', 'e': // CUD, VPR
		bot := t.rows - 1
		if c.Y <= t.bottom {
			bot = t.bottom
		}
		c.Y = min(c.Y+n, bot)
		c.pendingWrap = false
	case 'C', 'a': // CUF, HPR
		c.X = min(c.X+n, t.cols-1)
		c.pendingWrap = false
	case 'D': // CUB
		c.X = max(c.X-n, 0)
		c.pendingWrap = false
	case 'E': // CNL
		h.CsiDispatch(&vt.Seq{Params: s.Params}, 'B')
		t.carriageReturn()
	case 'F': // CPL
		h.CsiDispatch(&vt.Seq{Params: s.Params}, 'A')
		t.carriageReturn()
	case 'G', '`': // CHA, HPA
		c.X = clamp(n-1, 0, t.cols-1)
		c.pendingWrap = false
	case 'H', 'f': // CUP, HVP
		t.moveTo(int(p.Get(1, 1))-1, n-1)
	case 'I': // CHT
		t.tabForward(n)
	case 'J': // ED
		t.eraseDisplay(int(p.Raw(0)))
	case 'K': // EL
		t.eraseLine(int(p.Raw(0)))
	case 'L': // IL
		if c.Y >= t.top && c.Y <= t.bottom {
			t.scrollDownFrom(c.Y, n)
			t.carriageReturn()
		}
	case 'M': // DL
		if c.Y >= t.top && c.Y <= t.bottom {
			t.scrollUpFrom(c.Y, n)
			t.carriageReturn()
		}
	case 'P': // DCH
		t.deleteCells(n)
	case 'S': // SU
		t.scrollUp(n)
	case 'T': // SD
		t.scrollDown(n)
	case 'X': // ECH
		t.eraseCells(n)
	case 'Z': // CBT
		t.tabBackward(n)
	case 'b': // REP
		if t.lastRune != 0 {
			for range min(n, 65535) {
				t.print(t.lastRune)
			}
		}
	case 'c': // DA1: VT220 with ANSI color
		if p.Raw(0) == 0 {
			t.respond("\x1b[?62;22c")
		}
	case 'd': // VPA
		t.moveTo(c.X, n-1)
	case 'g': // TBC
		switch p.Raw(0) {
		case 0:
			t.tabs[c.X] = false
		case 3:
			clear(t.tabs)
		}
	case 'h', 'l': // SM, RM
		for i := 0; i < p.Len(); i++ {
			h.setANSIMode(int(p.Raw(i)), final == 'h')
		}
	case 'm':
		t.sgr(p)
	case 'n': // DSR
		switch p.Raw(0) {
		case 5:
			t.respond("\x1b[0n")
		case 6:
			t.respond(fmt.Sprintf("\x1b[%d;%dR", h.reportRow(), c.X+1))
		}
	case 'r': // DECSTBM
		top := int(p.Get(0, 1)) - 1
		bot := int(p.Get(1, uint16(t.rows))) - 1
		bot = min(bot, t.rows-1)
		if top < bot {
			t.top, t.bottom = top, bot
			t.moveTo(0, 0)
		}
	case 's': // SCOSC
		t.saveCursor()
	case 'u': // SCORC
		t.restoreCursor()
	case 't': // XTWINOPS
		if p.Raw(0) == 18 {
			t.respond(fmt.Sprintf("\x1b[8;%d;%dt", t.rows, t.cols))
		}
	}
}

func (h *handler) reportRow() int {
	t := h.t()
	if t.modes.Origin {
		return t.cur.Y - t.top + 1
	}
	return t.cur.Y + 1
}

func (h *handler) csiPrivate(s *vt.Seq, final byte) {
	t := h.t()
	p := &s.Params
	if s.Intermediate() == '$' && final == 'p' { // DECRQM (DEC modes)
		h.reportMode(int(p.Raw(0)), true)
		return
	}
	if s.Intermediate() != 0 {
		return
	}
	switch final {
	case 'h', 'l':
		for i := 0; i < p.Len(); i++ {
			h.setDECMode(int(p.Raw(i)), final == 'h')
		}
	case 'n':
		if p.Raw(0) == 6 { // DECXCPR
			t.respond(fmt.Sprintf("\x1b[?%d;%dR", h.reportRow(), t.cur.X+1))
		}
	case 'J': // DECSED (selective erase treated as plain erase)
		t.eraseDisplay(int(p.Raw(0)))
	case 'K': // DECSEL
		t.eraseLine(int(p.Raw(0)))
	case 'm': // XTQMODKEYS: CSI ? 4 m
		if p.Raw(0) == 4 {
			t.respond(fmt.Sprintf("\x1b[>4;%dm", t.modifyOtherKeys))
		}
	case 'u': // kitty keyboard: query flags
		t.respond(fmt.Sprintf("\x1b[?%du", t.KeyboardFlags()))
	}
}

func (h *handler) setANSIMode(mode int, on bool) {
	t := h.t()
	switch mode {
	case 4:
		t.modes.Insert = on
	case 20:
		t.modes.LineFeedNewLine = on
	}
}

func (h *handler) setDECMode(mode int, on bool) {
	t := h.t()
	m := &t.modes
	switch mode {
	case 1:
		m.AppCursor = on
	case 5:
		if m.ReverseVideo != on {
			m.ReverseVideo = on
			t.markAllDirty()
		}
	case 6:
		m.Origin = on
		t.moveTo(0, 0)
	case 7:
		m.Autowrap = on
		if !on {
			t.cur.pendingWrap = false
		}
	case 12:
		m.CursorBlink = on
	case 25:
		m.CursorVisible = on
	case 66:
		m.AppKeypad = on
	case 9, 1000, 1002, 1003:
		mm := map[int]MouseMode{9: MouseX10, 1000: MouseNormal, 1002: MouseButton, 1003: MouseAny}[mode]
		if on {
			m.Mouse = mm
		} else if m.Mouse == mm {
			m.Mouse = MouseNone
		}
	case 1005, 1006, 1015, 1016:
		enc := map[int]MouseEncoding{1005: MouseEncUTF8, 1006: MouseEncSGR, 1015: MouseEncURXVT, 1016: MouseEncSGRPixels}[mode]
		if on {
			m.MouseEncoding = enc
		} else if m.MouseEncoding == enc {
			m.MouseEncoding = MouseEncDefault
		}
	case 1004:
		m.FocusEvents = on
	case 47:
		if on {
			t.enterAltScreen(false)
		} else {
			t.exitAltScreen()
		}
	case 1047:
		if on {
			t.enterAltScreen(false)
		} else {
			if t.active == &t.alt {
				for y := range t.alt.lines {
					t.alt.lines[y] = t.resetLine(t.alt.lines[y])
				}
			}
			t.exitAltScreen()
		}
	case 1048:
		if on {
			t.saveCursor()
		} else {
			t.restoreCursor()
		}
	case 1049:
		if on {
			if t.active == &t.primary {
				t.saveCursor()
				t.enterAltScreen(true)
			}
		} else if t.active == &t.alt {
			t.exitAltScreen()
			t.restoreCursor()
		}
	case 1007:
		m.AltScroll = on
	case 2004:
		m.BracketedPaste = on
	case 2026:
		m.SyncOutput = on
	}
}

// modeState returns (set, known) for DECRQM.
func (h *handler) modeState(mode int, dec bool) (bool, bool) {
	t := h.t()
	m := &t.modes
	if !dec {
		switch mode {
		case 4:
			return m.Insert, true
		case 20:
			return m.LineFeedNewLine, true
		}
		return false, false
	}
	switch mode {
	case 1:
		return m.AppCursor, true
	case 5:
		return m.ReverseVideo, true
	case 6:
		return m.Origin, true
	case 7:
		return m.Autowrap, true
	case 12:
		return m.CursorBlink, true
	case 25:
		return m.CursorVisible, true
	case 66:
		return m.AppKeypad, true
	case 9:
		return m.Mouse == MouseX10, true
	case 1000:
		return m.Mouse == MouseNormal, true
	case 1002:
		return m.Mouse == MouseButton, true
	case 1003:
		return m.Mouse == MouseAny, true
	case 1004:
		return m.FocusEvents, true
	case 1005:
		return m.MouseEncoding == MouseEncUTF8, true
	case 1006:
		return m.MouseEncoding == MouseEncSGR, true
	case 1015:
		return m.MouseEncoding == MouseEncURXVT, true
	case 1016:
		return m.MouseEncoding == MouseEncSGRPixels, true
	case 47, 1047, 1049:
		return t.active == &t.alt, true
	case 1007:
		return m.AltScroll, true
	case 2004:
		return m.BracketedPaste, true
	case 2026:
		return m.SyncOutput, true
	}
	return false, false
}

func (h *handler) reportMode(mode int, dec bool) {
	set, known := h.modeState(mode, dec)
	state := 0 // not recognized
	if known {
		state = 2 // reset
		if set {
			state = 1
		}
	}
	prefix := ""
	if dec {
		prefix = "?"
	}
	h.t().respond(fmt.Sprintf("\x1b[%s%d;%d$y", prefix, mode, state))
}

func (h *handler) OscDispatch(data []byte) {
	t := h.t()
	num, rest := data, []byte(nil)
	for i, c := range data {
		if c == ';' {
			num, rest = data[:i], data[i+1:]
			break
		}
	}
	code, err := strconv.Atoi(string(num))
	if err != nil {
		return
	}
	switch code {
	case 0, 2: // set window title (0 also sets icon name)
		t.title = string(rest)
		if t.opts.OnTitle != nil {
			t.opts.OnTitle(t.title)
		}
	case 4, 10, 11, 12, 104, 110, 111, 112:
		t.oscColor(code, string(rest))
	case 7:
		t.oscCWD(string(rest))
	case 133:
		t.oscSemanticPrompt(string(rest))
	case 52:
		t.oscClipboard(string(rest))
	}

}

func (h *handler) DcsDispatch(s *vt.Seq, final byte, data []byte) {
	h.dcs(s, final, data)
}

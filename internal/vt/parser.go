// Package vt implements a VT500-compatible escape sequence parser.
//
// It follows Paul Williams' DEC parser state machine
// (https://vt100.net/emu/dec_ansi_parser) with two departures common to
// modern terminals: input is UTF-8 (8-bit C1 controls are not recognized),
// and runs of printable ASCII are delivered in one PrintASCII call so the
// hot path avoids a per-byte interface call.
package vt

import "unicode/utf8"

// Handler receives parsed actions. Slices passed to a Handler are only
// valid for the duration of the call.
type Handler interface {
	Print(r rune)
	PrintASCII(s []byte) // run of bytes in 0x20–0x7E
	Execute(c byte)      // C0 control
	EscDispatch(s *Seq, final byte)
	CsiDispatch(s *Seq, final byte)
	OscDispatch(data []byte)
	DcsDispatch(s *Seq, final byte, data []byte)
}

type state uint8

const (
	sGround state = iota
	sEscape
	sEscInter
	sCsiEntry
	sCsiParam
	sCsiInter
	sCsiIgnore
	sDcsEntry
	sDcsParam
	sDcsInter
	sDcsPass
	sDcsIgnore
	sOsc
	sSosPmApc
)

// Limits on string payloads; anything beyond is dropped.
const (
	maxOSC = 1 << 20 // OSC 52 clipboard payloads can be large
	maxDCS = 1 << 16
)

// Parser is a streaming escape sequence parser. It is not safe for
// concurrent use.
type Parser struct {
	h        Handler
	st       state
	seq      Seq
	buf      []byte // OSC or DCS payload
	dcsFinal byte

	u8  [utf8.UTFMax]byte // partial UTF-8 sequence split across Advance calls
	u8n int
}

// New returns a parser that delivers actions to h.
func New(h Handler) *Parser {
	return &Parser{h: h, buf: make([]byte, 0, 256)}
}

// Advance feeds bytes to the parser.
func (p *Parser) Advance(b []byte) {
	i := 0
	if p.u8n > 0 {
		i = p.finishUTF8(b)
	}
	for i < len(b) {
		if p.st != sGround {
			p.step(b[i])
			i++
			continue
		}
		// Fast path: printable ASCII run.
		j := i
		for j < len(b) && b[j] >= 0x20 && b[j] < 0x7f {
			j++
		}
		if j > i {
			p.h.PrintASCII(b[i:j])
			i = j
			continue
		}
		c := b[i]
		switch {
		case c == 0x1b:
			p.enterEscape()
			i++
		case c < 0x20:
			p.h.Execute(c)
			i++
		case c == 0x7f:
			i++ // DEL is ignored
		default:
			r, size := utf8.DecodeRune(b[i:])
			if r == utf8.RuneError && size <= 1 {
				if !utf8.FullRune(b[i:]) {
					// Valid prefix cut off at the end of the buffer.
					p.u8n = copy(p.u8[:], b[i:])
					return
				}
				p.h.Print(utf8.RuneError)
				i++
				continue
			}
			p.h.Print(r)
			i += size
		}
	}
}

// finishUTF8 completes a rune buffered from the previous Advance call and
// returns how many bytes of b it consumed.
func (p *Parser) finishUTF8(b []byte) int {
	i := 0
	for i < len(b) {
		c := b[i]
		if c&0xc0 != 0x80 { // not a continuation byte: the prefix was bad
			p.u8n = 0
			p.h.Print(utf8.RuneError)
			return i
		}
		p.u8[p.u8n] = c
		p.u8n++
		i++
		if utf8.FullRune(p.u8[:p.u8n]) {
			r, _ := utf8.DecodeRune(p.u8[:p.u8n])
			p.u8n = 0
			p.h.Print(r) // RuneError for invalid encodings
			return i
		}
	}
	return i
}

func (p *Parser) enterEscape() {
	p.seq.reset()
	p.st = sEscape
}

func (p *Parser) step(c byte) {
	// Transitions valid from any state.
	switch c {
	case 0x18, 0x1a: // CAN, SUB: abort the sequence
		p.st = sGround
		return
	case 0x1b:
		switch p.st {
		case sOsc:
			p.dispatchOSC()
		case sDcsPass:
			p.dispatchDCS()
		}
		p.enterEscape()
		return
	}

	switch p.st {
	case sEscape:
		switch {
		case c < 0x20:
			p.h.Execute(c)
		case c < 0x30:
			p.seq.collect(c)
			p.st = sEscInter
		case c == '[':
			p.seq.reset()
			p.st = sCsiEntry
		case c == ']':
			p.buf = p.buf[:0]
			p.st = sOsc
		case c == 'P':
			p.seq.reset()
			p.buf = p.buf[:0]
			p.st = sDcsEntry
		case c == 'X', c == '^', c == '_': // SOS, PM, APC
			p.st = sSosPmApc
		case c == 'k':
			// screen/tmux "set window name" (ESC k name ST). Apps send it
			// when TERM is screen* or tmux* — always the case for panes in
			// tmux control mode — so swallow the string instead of
			// printing it.
			p.st = sSosPmApc
		case c < 0x7f:
			p.st = sGround
			p.h.EscDispatch(&p.seq, c)
		}

	case sEscInter:
		switch {
		case c < 0x20:
			p.h.Execute(c)
		case c < 0x30:
			p.seq.collect(c)
		case c < 0x7f:
			p.st = sGround
			if !p.seq.overflow {
				p.h.EscDispatch(&p.seq, c)
			}
		}

	case sCsiEntry, sCsiParam, sCsiInter:
		p.csi(c)

	case sCsiIgnore:
		switch {
		case c < 0x20:
			p.h.Execute(c)
		case c >= 0x40 && c < 0x7f:
			p.st = sGround
		}

	case sDcsEntry, sDcsParam, sDcsInter:
		p.dcsHeader(c)

	case sDcsPass:
		if c != 0x7f && len(p.buf) < maxDCS {
			p.buf = append(p.buf, c)
		}

	case sOsc:
		switch {
		case c == 0x07: // BEL terminator (xterm)
			p.dispatchOSC()
			p.st = sGround
		case c < 0x20:
			// ignored
		default:
			if len(p.buf) < maxOSC {
				p.buf = append(p.buf, c)
			}
		}

	case sDcsIgnore, sSosPmApc:
		// Swallow until ST (handled by the ESC transition above).
	}
}

// csi handles the CsiEntry, CsiParam, and CsiInter states.
func (p *Parser) csi(c byte) {
	switch {
	case c < 0x20:
		p.h.Execute(c)
	case c < 0x30: // intermediate
		p.seq.collect(c)
		p.st = sCsiInter
	case c < 0x40: // parameter bytes
		if p.st == sCsiInter {
			p.st = sCsiIgnore
			return
		}
		if !p.param(c) {
			p.st = sCsiIgnore
			return
		}
		p.st = sCsiParam
	case c < 0x7f: // final
		p.st = sGround
		if !p.seq.overflow {
			p.h.CsiDispatch(&p.seq, c)
		}
	}
}

// dcsHeader handles DcsEntry, DcsParam, and DcsInter.
func (p *Parser) dcsHeader(c byte) {
	switch {
	case c < 0x20:
		// C0 controls are ignored in DCS headers
	case c < 0x30:
		p.seq.collect(c)
		p.st = sDcsInter
	case c < 0x40:
		if p.st == sDcsInter || !p.param(c) {
			p.st = sDcsIgnore
			return
		}
		p.st = sDcsParam
	case c < 0x7f:
		if p.seq.overflow {
			p.st = sDcsIgnore
			return
		}
		p.dcsFinal = c
		p.st = sDcsPass
	}
}

// param consumes a byte in 0x30–0x3F. It returns false if the sequence is
// malformed and should be ignored.
func (p *Parser) param(c byte) bool {
	switch {
	case c <= '9':
		p.seq.Params.digit(c)
	case c == ';':
		p.seq.Params.sep(false)
	case c == ':':
		p.seq.Params.sep(true)
	default: // '<', '=', '>', '?': private marker, only valid first
		if p.st != sCsiEntry && p.st != sDcsEntry {
			return false
		}
		p.seq.Prefix = c
	}
	return true
}

func (p *Parser) dispatchOSC() {
	p.h.OscDispatch(p.buf)
	p.buf = p.buf[:0]
}

func (p *Parser) dispatchDCS() {
	p.h.DcsDispatch(&p.seq, p.dcsFinal, p.buf)
	p.buf = p.buf[:0]
}

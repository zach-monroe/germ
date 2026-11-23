package vt

// MaxParams is the maximum number of parameters (including colon
// sub-parameters) kept for one sequence. Extra parameters are dropped.
const MaxParams = 32

// Params holds the numeric parameters of a CSI or DCS sequence.
//
// Empty parameters are stored as 0; handlers treat 0 as "use the default".
// Colon-separated sub-parameters (e.g. SGR "38:2::255:0:0" or "4:3") are
// stored inline and flagged with IsSub.
type Params struct {
	vals [MaxParams]uint16
	sub  [MaxParams]bool
	n    int
	full bool
}

// Len returns the number of parameters, including sub-parameters.
func (p *Params) Len() int { return p.n }

// Get returns parameter i, or def if it is missing or zero.
func (p *Params) Get(i int, def uint16) uint16 {
	if i >= p.n || p.vals[i] == 0 {
		return def
	}
	return p.vals[i]
}

// Raw returns parameter i as stored (0 if missing).
func (p *Params) Raw(i int) uint16 {
	if i < 0 || i >= p.n {
		return 0
	}
	return p.vals[i]
}

// IsSub reports whether parameter i was introduced by ':' (a sub-parameter
// of the preceding parameter).
func (p *Params) IsSub(i int) bool { return i >= 0 && i < p.n && p.sub[i] }

func (p *Params) reset() { p.n, p.full = 0, false }

func (p *Params) start() {
	if p.n == 0 {
		p.n = 1
		p.vals[0], p.sub[0] = 0, false
	}
}

func (p *Params) digit(d byte) {
	if p.full {
		return
	}
	p.start()
	v := uint32(p.vals[p.n-1])*10 + uint32(d-'0')
	if v > 0xffff {
		v = 0xffff
	}
	p.vals[p.n-1] = uint16(v)
}

func (p *Params) sep(colon bool) {
	if p.full {
		return
	}
	p.start()
	if p.n == MaxParams {
		p.full = true
		return
	}
	p.vals[p.n], p.sub[p.n] = 0, colon
	p.n++
}

// Seq is the parsed header of an ESC, CSI, or DCS sequence.
type Seq struct {
	Params Params
	// Prefix is the private-marker byte ('?', '>', '<', '=') of a CSI/DCS
	// sequence, or 0 if none.
	Prefix   byte
	inter    [2]byte
	ni       int
	overflow bool
}

// Inter returns the intermediate bytes (0x20–0x2F), e.g. "$" in "CSI ? 2026 $ p".
func (s *Seq) Inter() []byte { return s.inter[:s.ni] }

// Intermediate returns the first intermediate byte, or 0.
func (s *Seq) Intermediate() byte {
	if s.ni == 0 {
		return 0
	}
	return s.inter[0]
}

func (s *Seq) reset() {
	s.Params.reset()
	s.Prefix, s.ni, s.overflow = 0, 0, false
}

func (s *Seq) collect(c byte) {
	if s.ni == len(s.inter) {
		s.overflow = true
		return
	}
	s.inter[s.ni] = c
	s.ni++
}

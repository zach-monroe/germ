package term

// Scrollback is a ring buffer of lines that scrolled off the top of the
// primary screen. Pushing into a full buffer recycles the oldest line's
// storage so steady-state scrolling does not allocate.
type Scrollback struct {
	lines []Line
	start int // index of the oldest line
	n     int
	max   int
}

func newScrollback(max int) *Scrollback {
	return &Scrollback{max: max}
}

// Len returns the number of lines held.
func (s *Scrollback) Len() int { return s.n }

// Max returns the capacity.
func (s *Scrollback) Max() int { return s.max }

// At returns line i, where 0 is the oldest.
func (s *Scrollback) At(i int) *Line {
	return &s.lines[(s.start+i)%len(s.lines)]
}

// push appends l and returns a Line whose storage the caller may reuse
// (the evicted oldest line, or an empty Line).
func (s *Scrollback) push(l Line) Line {
	if s.max == 0 {
		return l
	}
	if s.n < s.max {
		if s.n == len(s.lines) {
			// Still growing; start is 0 until the ring first fills.
			s.lines = append(s.lines, l)
			s.n++
			return Line{}
		}
		s.lines[(s.start+s.n)%len(s.lines)] = l
		s.n++
		return Line{}
	}
	old := s.lines[s.start]
	s.lines[s.start] = l
	s.start = (s.start + 1) % len(s.lines)
	return old
}

func (s *Scrollback) clear() {
	clear(s.lines)
	s.lines = s.lines[:0]
	s.start, s.n = 0, 0
}

package vt

import (
	"fmt"
	"strings"
	"testing"
)

// recorder renders every action as a short string for easy comparison.
type recorder struct{ out []string }

func (r *recorder) Print(c rune)        { r.out = append(r.out, fmt.Sprintf("print %q", c)) }
func (r *recorder) PrintASCII(s []byte) { r.out = append(r.out, fmt.Sprintf("ascii %q", s)) }
func (r *recorder) Execute(c byte)      { r.out = append(r.out, fmt.Sprintf("exec %#02x", c)) }
func (r *recorder) EscDispatch(s *Seq, f byte) {
	r.out = append(r.out, fmt.Sprintf("esc %q %c", s.Inter(), f))
}
func (r *recorder) CsiDispatch(s *Seq, f byte) {
	r.out = append(r.out, "csi "+seqString(s)+string(f))
}
func (r *recorder) OscDispatch(d []byte) { r.out = append(r.out, fmt.Sprintf("osc %q", d)) }
func (r *recorder) DcsDispatch(s *Seq, f byte, d []byte) {
	r.out = append(r.out, fmt.Sprintf("dcs %s%c %q", seqString(s), f, d))
}

func seqString(s *Seq) string {
	var b strings.Builder
	if s.Prefix != 0 {
		b.WriteByte(s.Prefix)
	}
	for i := 0; i < s.Params.Len(); i++ {
		if i > 0 {
			if s.Params.IsSub(i) {
				b.WriteByte(':')
			} else {
				b.WriteByte(';')
			}
		}
		fmt.Fprint(&b, s.Params.Raw(i))
	}
	b.Write(s.Inter())
	return b.String()
}

func parse(chunks ...string) []string {
	r := &recorder{}
	p := New(r)
	for _, c := range chunks {
		p.Advance([]byte(c))
	}
	return r.out
}

func TestParser(t *testing.T) {
	tests := []struct {
		name   string
		chunks []string
		want   []string
	}{
		{"ascii run", []string{"hello"}, []string{`ascii "hello"`}},
		{"controls", []string{"a\r\nb"}, []string{`ascii "a"`, "exec 0x0d", "exec 0x0a", `ascii "b"`}},
		{"utf8", []string{"é世"}, []string{`print 'é'`, `print '世'`}},
		{"utf8 split", []string{"\xe4\xb8", "\x96x"}, []string{`print '世'`, `ascii "x"`}},
		{"utf8 split 3 ways", []string{"\xf0", "\x9f\x98", "\x80"}, []string{`print '😀'`}},
		{"invalid utf8", []string{"\xffa"}, []string{`print '�'`, `ascii "a"`}},
		{"truncated utf8", []string{"\xe4", "a"}, []string{`print '�'`, `ascii "a"`}},
		{"csi no params", []string{"\x1b[H"}, []string{"csi H"}},
		{"csi params", []string{"\x1b[12;34H"}, []string{"csi 12;34H"}},
		{"csi empty params", []string{"\x1b[;5H"}, []string{"csi 0;5H"}},
		{"csi private", []string{"\x1b[?1049h"}, []string{"csi ?1049h"}},
		{"csi intermediate", []string{"\x1b[?2026$p"}, []string{"csi ?2026$p"}},
		{"csi space intermediate", []string{"\x1b[2 q"}, []string{"csi 2 q"}},
		{"csi subparams", []string{"\x1b[38:2::1:2:3m"}, []string{"csi 38:2:0:1:2:3m"}},
		{"csi split", []string{"\x1b[3", "1m"}, []string{"csi 31m"}},
		{"csi with embedded control", []string{"\x1b[1\n2H"}, []string{"exec 0x0a", "csi 12H"}},
		{"csi bad private position", []string{"\x1b[1?hX"}, []string{`ascii "X"`}},
		{"csi cancel", []string{"\x1b[12\x18X"}, []string{`ascii "X"`}},
		{"esc", []string{"\x1b7\x1b8"}, []string{`esc "" 7`, `esc "" 8`}},
		{"esc charset", []string{"\x1b(0"}, []string{`esc "(" 0`}},
		{"osc bel", []string{"\x1b]0;title\x07"}, []string{`osc "0;title"`}},
		{"osc st", []string{"\x1b]2;hi\x1b\\"}, []string{`osc "2;hi"`, `esc "" \`}},
		{"osc utf8", []string{"\x1b]2;né\x07"}, []string{`osc "2;né"`}},
		{"dcs", []string{"\x1bP1$qm\x1b\\"}, []string{`dcs 1$q "m"`, `esc "" \`}},
		{"screen title ignored", []string{"\x1bkecho\x1b\\x"}, []string{`esc "" \`, `ascii "x"`}},
		{"apc ignored", []string{"\x1b_Gf=1;AAAA\x1b\\x"}, []string{`esc "" \`, `ascii "x"`}},
		{"param overflow saturates", []string{"\x1b[99999999A"}, []string{"csi 65535A"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parse(tt.chunks...)
			if strings.Join(got, "|") != strings.Join(tt.want, "|") {
				t.Errorf("got  %q\nwant %q", got, tt.want)
			}
		})
	}
}

func TestTooManyParams(t *testing.T) {
	seq := "\x1b[" + strings.Repeat("1;", 40) + "m"
	got := parse(seq)
	if len(got) != 1 || !strings.HasPrefix(got[0], "csi 1;") {
		t.Fatalf("got %q", got)
	}
}

type nopHandler struct{}

func (nopHandler) Print(rune)                     {}
func (nopHandler) PrintASCII([]byte)              {}
func (nopHandler) Execute(byte)                   {}
func (nopHandler) EscDispatch(*Seq, byte)         {}
func (nopHandler) CsiDispatch(*Seq, byte)         {}
func (nopHandler) OscDispatch([]byte)             {}
func (nopHandler) DcsDispatch(*Seq, byte, []byte) {}

func benchInput() []byte {
	var b strings.Builder
	for b.Len() < 1<<20 {
		b.WriteString("\x1b[1;32mfile.go\x1b[0m  \x1b[38;2;10;20;30mrgb text\x1b[m some plain ascii output here\r\n")
	}
	return []byte(b.String())
}

func BenchmarkParser(b *testing.B) {
	in := benchInput()
	p := New(nopHandler{})
	b.SetBytes(int64(len(in)))
	for b.Loop() {
		p.Advance(in)
	}
}

package font

import (
	"image"
	"testing"
)

func TestSlantAndEmbolden(t *testing.T) {
	// A one-pixel vertical stroke from y=0 to y=9; baseline at 10.
	m := image.NewAlpha(image.Rect(0, 0, 1, 10))
	for y := 0; y < 10; y++ {
		m.Pix[y*m.Stride] = 255
	}
	b := image.Rect(5, 0, 6, 10)
	sm, sb := slant(m, b, 10)
	if sb.Dy() != 10 || sb.Dx() <= 1 {
		t.Fatalf("slant bounds %v", sb)
	}
	// The top of the stroke moved right of the bottom.
	col := func(row int) float64 {
		var sum, wsum float64
		for x := 0; x < sb.Dx(); x++ {
			a := float64(sm.Pix[row*sm.Stride+x])
			sum += a * float64(x)
			wsum += a
		}
		return sum / wsum
	}
	if !(col(0) > col(9)+1) {
		t.Errorf("not slanted: top at %.2f, bottom at %.2f", col(0), col(9))
	}
	// Ink is preserved (within rounding).
	total := 0
	for _, a := range sm.Pix {
		total += int(a)
	}
	if total < 255*10-20 || total > 255*10+20 {
		t.Errorf("ink changed: %d", total)
	}

	em, eb := embolden(m, b, 1)
	if eb.Dx() != 2 || em.Pix[0] != 255 || em.Pix[1] != 255 {
		t.Errorf("embolden: %v %v", eb, em.Pix[:2])
	}
}

package font

import (
	"os/exec"
	"strings"
	"testing"
)

func TestLigatures(t *testing.T) {
	out, _ := exec.Command("fc-match", "-f", "%{family}", "JetBrainsMono Nerd Font").Output()
	fam := strings.Split(string(out), ",")[0]
	if !strings.Contains(fam, "JetBrains") {
		t.Skip("JetBrains Mono not installed")
	}
	s, err := Load(fam, 14, 96)
	if err != nil {
		t.Fatal(err)
	}
	ids := s.Ligatures([]rune("->"), Regular)
	if ids == nil || len(ids) != 2 {
		t.Fatalf("no ligature for -> in %s", fam)
	}
	if g, ok := s.GlyphByID(ids[1], Regular); !ok || g.Mask == nil || g.Bounds.Empty() {
		t.Errorf("ligature glyph didn't rasterize: %+v", g.Bounds)
	}
	for _, plain := range []string{"ab", "x"} {
		if s.Ligatures([]rune(plain), Regular) != nil {
			t.Errorf("%q shaped", plain)
		}
	}
	if s.Ligatures([]rune("!="), Bold) == nil {
		t.Error("no bold ligature for !=")
	}
	// Cached.
	if len(s.ligs) == 0 {
		t.Error("nothing cached")
	}
}

package app

import (
	"testing"

	"github.com/zach-monroe/germ/internal/config"
)

func TestParseWidgetSpec(t *testing.T) {
	if n, w := parseWidgetSpec("news:3"); n != "news" || w != 3 {
		t.Errorf("got %s %v", n, w)
	}
	if n, w := parseWidgetSpec(" clock "); n != "clock" || w != 1 {
		t.Errorf("got %s %v", n, w)
	}
	if _, w := parseWidgetSpec("x:-1"); w != 1 {
		t.Errorf("bad weight accepted: %v", w)
	}
}

func TestChainWeights(t *testing.T) {
	root := chain([]*node{leaf(1), leaf(2), leaf(3)}, []float64{2, 1, 1}, splitH)
	root.layout(cellRect{0, 0, 83, 10})
	var widths []int
	for _, l := range root.leaves(nil) {
		widths = append(widths, l.rect.W)
	}
	// 83 cells minus 2 separators = 81: about 40 / 20 / 20.
	if widths[0] < 39 || widths[0] > 41 || widths[1] < 19 || widths[1] > 21 {
		t.Errorf("widths %v", widths)
	}
}

func TestWidgetConfig(t *testing.T) {
	cfg, _, err := config.Parse([]byte(`
[widgets.weather]
location = "Denver"
[widgets.stocks]
symbols = ["AAPL", "TSLA"]
[widgets.mine]
command = "echo hi"
interval = "30s"
`))
	if err != nil {
		t.Fatal(err)
	}
	w, err := cfg.Widget("stocks")
	if err != nil || len(w.Env) != 1 || w.Env[0] != "GERM_WIDGET_SYMBOLS=AAPL,TSLA" {
		t.Errorf("stocks %+v %v", w, err)
	}
	m, err := cfg.Widget("mine")
	if err != nil || len(m.Command) != 3 || m.Command[2] != "echo hi" || m.Interval.Seconds() != 30 {
		t.Errorf("mine %+v %v", m, err)
	}
	if !cfg.Home.Enabled || len(cfg.Home.Layout) != 2 {
		t.Errorf("home defaults %+v", cfg.Home)
	}
}

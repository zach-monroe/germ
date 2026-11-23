package widgets

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"strings"
	"sync"
	"time"
)

// stocks tracks symbols with price, daily change, and an intraday
// sparkline (Yahoo Finance chart data, no key). Options: symbols
// (comma-separated, e.g. "AAPL,NVDA,BTC-USD").
type stocks struct {
	mu      sync.Mutex
	symbols []string
	quotes  map[string]*quote
	updated time.Time
}

type quote struct {
	price, prev float64
	series      []float64
	currency    string
}

func newStocks() Widget {
	var syms []string
	for _, s := range strings.Split(Option("symbols", "SPY,QQQ,AAPL,NVDA,MSFT,BTC-USD"), ",") {
		if s = strings.ToUpper(strings.TrimSpace(s)); s != "" {
			syms = append(syms, s)
		}
	}
	return &stocks{symbols: syms, quotes: map[string]*quote{}}
}

func (s *stocks) Every() time.Duration { return time.Minute }
func (s *stocks) Tick() time.Duration  { return 0 }

func (s *stocks) Update(ctx context.Context) error {
	var firstErr error
	for _, sym := range s.symbols {
		q, err := fetchQuote(ctx, sym)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		s.mu.Lock()
		s.quotes[sym] = q
		s.mu.Unlock()
	}
	s.mu.Lock()
	s.updated = time.Now()
	s.mu.Unlock()
	return firstErr
}

func fetchQuote(ctx context.Context, sym string) (*quote, error) {
	body, err := get(ctx, "https://query1.finance.yahoo.com/v8/finance/chart/"+url.PathEscape(sym)+"?range=1d&interval=5m")
	if err != nil {
		return nil, fmt.Errorf("%s: %w", sym, err)
	}
	var r struct {
		Chart struct {
			Result []struct {
				Meta struct {
					Price     float64 `json:"regularMarketPrice"`
					PrevClose float64 `json:"chartPreviousClose"`
					Previous  float64 `json:"previousClose"`
					Currency  string  `json:"currency"`
				} `json:"meta"`
				Indicators struct {
					Quote []struct {
						Close []*float64 `json:"close"`
					} `json:"quote"`
				} `json:"indicators"`
			} `json:"result"`
		} `json:"chart"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, err
	}
	if len(r.Chart.Result) == 0 {
		return nil, fmt.Errorf("%s: no data", sym)
	}
	res := r.Chart.Result[0]
	q := &quote{price: res.Meta.Price, prev: res.Meta.PrevClose, currency: res.Meta.Currency}
	if q.prev == 0 {
		q.prev = res.Meta.Previous
	}
	if len(res.Indicators.Quote) > 0 {
		for _, c := range res.Indicators.Quote[0].Close {
			if c != nil {
				q.series = append(q.series, *c)
			}
		}
	}
	return q, nil
}

// sparkline draws values as w block characters.
func sparkline(vals []float64, w int) string {
	if len(vals) == 0 || w <= 0 {
		return ""
	}
	blocks := []rune("▁▂▃▄▅▆▇█")
	lo, hi := math.Inf(1), math.Inf(-1)
	for _, v := range vals {
		lo, hi = math.Min(lo, v), math.Max(hi, v)
	}
	out := make([]rune, w)
	for i := range out {
		// Sample the series at evenly spaced points.
		v := vals[min(len(vals)-1, i*len(vals)/w)]
		k := 0
		if hi > lo {
			k = int((v - lo) / (hi - lo) * float64(len(blocks)-1))
		}
		out[i] = blocks[k]
	}
	return string(out)
}

func money(v float64) string {
	switch {
	case v >= 10000:
		return fmt.Sprintf("%.0f", v)
	case v >= 100:
		return fmt.Sprintf("%.2f", v)
	}
	return fmt.Sprintf("%.3f", v)
}

func (s *stocks) Draw(w, h int, err error) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	info := ""
	if !s.updated.IsZero() {
		info = s.updated.Format("15:04")
	}
	lines := []string{title(w, "↗", "stocks", info), ""}
	if len(s.quotes) == 0 {
		if err == nil {
			lines = append(lines, center(dim+"loading…"+reset, w))
		}
		return fill(lines, h, errLine(err, w))
	}
	// Columns: symbol(9) price(10) change(9) sparkline(rest).
	sparkW := w - 32
	spaced := h-3 >= 2*len(s.symbols) // room for a gap between rows
	for i, sym := range s.symbols {
		q := s.quotes[sym]
		if q == nil {
			continue
		}
		chg := 0.0
		if q.prev != 0 {
			chg = (q.price - q.prev) / q.prev * 100
		}
		c, arrow := green, "▲"
		if chg < 0 {
			c, arrow = red, "▼"
		}
		row := fmt.Sprintf("%s%-8s%s %10s %s%s%6.2f%%%s", bold, truncate(sym, 8), reset, money(q.price), c, arrow, math.Abs(chg), reset)
		row = " " + link("https://finance.yahoo.com/quote/"+url.PathEscape(sym), row)
		if sparkW >= 6 {
			row += "  " + c + sparkline(q.series, sparkW) + reset
		}
		if spaced && i > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, row)
		if len(lines) >= h-1 {
			break
		}
	}
	return fill(lines, h, errLine(err, w))
}

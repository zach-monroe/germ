package widgets

import (
	"context"
	"strings"
	"time"
)

// clock draws big block digits, the date, and a day-progress bar.
type clock struct {
	h24     bool
	seconds bool
}

func newClock() Widget {
	return &clock{
		h24:     Option("format", "24h") != "12h",
		seconds: Option("seconds", "true") != "false",
	}
}

func (c *clock) Update(context.Context) error { return nil }
func (c *clock) Every() time.Duration         { return 0 }
func (c *clock) Tick() time.Duration          { return time.Second }

// Digits are 3×5 pixels; each pixel becomes a block of cells.
var digitFont = map[rune][5]string{
	'0': {"###", "# #", "# #", "# #", "###"},
	'1': {" # ", "## ", " # ", " # ", "###"},
	'2': {"###", "  #", "###", "#  ", "###"},
	'3': {"###", "  #", " ##", "  #", "###"},
	'4': {"# #", "# #", "###", "  #", "  #"},
	'5': {"###", "#  ", "###", "  #", "###"},
	'6': {"###", "#  ", "###", "# #", "###"},
	'7': {"###", "  #", "  #", " # ", " # "},
	'8': {"###", "# #", "###", "# #", "###"},
	'9': {"###", "# #", "###", "  #", "###"},
	':': {" ", "#", " ", "#", " "},
	' ': {" ", " ", " ", " ", " "},
}

// bigText renders s with each font pixel sx cells wide. With half=true,
// two pixel rows share one cell row (▀ ▄ █), halving the height.
func bigText(s string, sx int, half bool) []string {
	rows := make([]strings.Builder, 5)
	for i, r := range s {
		g := digitFont[r]
		for y := 0; y < 5; y++ {
			if i > 0 {
				rows[y].WriteString(strings.Repeat(" ", sx))
			}
			for _, p := range g[y] {
				ch := " "
				if p == '#' {
					ch = "█"
				}
				rows[y].WriteString(strings.Repeat(ch, sx))
			}
		}
	}
	out := make([]string, 5)
	for y := range rows {
		out[y] = rows[y].String()
	}
	if !half {
		return out
	}
	// Merge pairs of rows: 5 rows -> 3 (the last pairs with blank).
	merged := []string{}
	for y := 0; y < 5; y += 2 {
		top := []rune(out[y])
		var bot []rune
		if y+1 < 5 {
			bot = []rune(out[y+1])
		}
		var b strings.Builder
		for x := range top {
			t := top[x] == '█'
			bt := x < len(bot) && bot[x] == '█'
			switch {
			case t && bt:
				b.WriteRune('█')
			case t:
				b.WriteRune('▀')
			case bt:
				b.WriteRune('▄')
			default:
				b.WriteRune(' ')
			}
		}
		merged = append(merged, b.String())
	}
	return merged
}

func (c *clock) Draw(w, h int, _ error) []string {
	now := time.Now()
	layout := "15:04"
	if !c.h24 {
		layout = "3:04"
	}
	if c.seconds {
		layout += ":05"
	}
	text := now.Format(layout)
	if now.Second()%2 == 1 && !c.seconds {
		text = strings.ReplaceAll(text, ":", " ") // blinking colon
	}
	ampm := ""
	if !c.h24 {
		ampm = now.Format("PM")
	}

	// Pick the biggest rendering that fits, leaving room for the date.
	var big []string
	for _, opt := range []struct {
		sx   int
		half bool
	}{{3, false}, {2, false}, {2, true}, {1, false}, {1, true}} {
		b := bigText(text, opt.sx, opt.half)
		if width(b[0]) <= w-2 && len(b)+3 <= h {
			big = b
			break
		}
	}
	date := now.Format("Monday, January 2 2006")
	if big == nil { // too small for big digits
		return []string{center(bold+cyan+text+" "+ampm+reset, w), center(dim+date+reset, w)}
	}

	var lines []string
	top := (h - len(big) - 3) / 2
	for i := 0; i < top; i++ {
		lines = append(lines, "")
	}
	for i, row := range big {
		color := cyan
		if i >= len(big)/2+1 {
			color = blue // a little vertical gradient
		}
		suffix := ""
		if i == len(big)-1 && ampm != "" {
			suffix = " " + dim + ampm + reset
		}
		lines = append(lines, center(bold+color+row+reset+suffix, w))
	}
	lines = append(lines, "", center(white+date+reset, w))

	// Day progress: how much of today has passed.
	barW := min(width(big[0]), w-4)
	y, m, d := now.Date()
	frac := float64(now.Sub(time.Date(y, m, d, 0, 0, 0, 0, now.Location()))) / float64(24*time.Hour)
	filled := int(frac*float64(barW) + 0.5)
	bar := magenta + strings.Repeat("━", filled) + dim + strings.Repeat("─", barW-filled) + reset
	lines = append(lines, center(bar, w))
	return lines
}

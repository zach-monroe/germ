package widgets

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// calendar shows your next events from iCalendar feeds: the secret
// "iCal address" Google Calendar, Outlook, Fastmail, iCloud... give you,
// or a local .ics file. No account or API key. Options: urls = [...]
// (feeds or file paths), days = 2 (how far ahead), format = "12h".
type calendar struct {
	urls   []string
	days   int
	format string
	fetch  func(ctx context.Context, url string) ([]byte, error) // for tests
	now    func() time.Time

	mu      sync.Mutex
	events  []icalEvent
	fetched time.Time
	errs    []string
}

func newCalendar(o Options) Widget {
	c := &calendar{days: atoiOr(o.Get("days", "2"), 2), format: o.Get("format", "24h"), fetch: fetchICal, now: time.Now}
	for _, u := range strings.Split(o.Get("urls", ""), ",") {
		if u = strings.TrimSpace(u); u != "" {
			c.urls = append(c.urls, u)
		}
	}
	c.days = min(max(c.days, 1), 14)
	return c
}

func (c *calendar) Every() time.Duration { return 10 * time.Minute }
func (c *calendar) Tick() time.Duration  { return 30 * time.Second } // countdowns

// fetchICal reads a feed: http(s) or webcal URL, or a file.
func fetchICal(ctx context.Context, u string) ([]byte, error) {
	if rest, ok := strings.CutPrefix(u, "webcal://"); ok {
		u = "https://" + rest
	}
	if strings.HasPrefix(u, "https://") || strings.HasPrefix(u, "http://") {
		return get(ctx, u)
	}
	return os.ReadFile(expandTilde(strings.TrimPrefix(u, "file://")))
}

func (c *calendar) Update(ctx context.Context) error {
	if len(c.urls) == 0 {
		return nil
	}
	var all []icalEvent
	var errs []string
	for _, u := range c.urls {
		b, err := c.fetch(ctx, u)
		if err == nil {
			var evs []icalEvent
			if evs, err = parseICal(string(b)); err == nil {
				all = append(all, evs...)
				continue
			}
		}
		errs = append(errs, feedName(u)+": "+err.Error())
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(errs) < len(c.urls) || c.fetched.IsZero() {
		c.events, c.fetched = all, time.Now()
	} // else every feed failed: keep showing the last good ones
	c.errs = errs
	if len(errs) > 0 {
		return errors.New(errs[0])
	}
	return nil
}

// feedName names a feed in errors without printing its secret URL.
func feedName(u string) string {
	if i := strings.Index(u, "://"); i >= 0 {
		host, _, _ := strings.Cut(u[i+3:], "/")
		return host
	}
	return u
}

func (c *calendar) Draw(w, h int, err error) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	lines := []string{title(w, "▦", "calendar", now.Format("Mon Jan 2"))}
	if len(c.urls) == 0 {
		lines = append(lines, "", dim+"  Add your calendar's iCal address:"+reset)
		if h > 5 {
			lines = append(lines, "", dim+"  [widgets.calendar]"+reset,
				dim+`  urls = ["https://…/basic.ics"]`+reset)
		}
		return fill(lines, h, "")
	}
	if c.fetched.IsZero() {
		if err == nil {
			lines = append(lines, "", center(dim+"loading…"+reset, w))
		}
		return fill(lines, h, errLine(err, w))
	}
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	end := today.AddDate(0, 0, c.days)
	occ := occurrences(c.events, now, end)

	limit := h - boolInt(err != nil)
	var allDay []string
	var timed []occurrence
	for _, o := range occ {
		if o.allDay && !o.start.After(today) { // today's (or ongoing): up top
			allDay = append(allDay, o.summary)
			continue
		}
		timed = append(timed, o) // later all-day events go under their day
	}
	if len(allDay) > 0 {
		lines = append(lines, " "+magenta+truncate("◆ "+strings.Join(allDay, " · "), w-2)+reset)
	}
	lines = append(lines, "")
	if len(timed) == 0 {
		lines = append(lines, dim+fmt.Sprintf("  Nothing in the next %s.", plural(c.days, "day"))+reset)
		return fill(lines, h, errLine(err, w))
	}
	day := today
	first := true
	for _, o := range timed {
		if len(lines) >= limit {
			break
		}
		od := time.Date(o.start.Year(), o.start.Month(), o.start.Day(), 0, 0, 0, 0, time.Local)
		if od.After(day) {
			day = od
			if len(lines) >= limit-1 {
				break
			}
			label := "tomorrow"
			if od.After(today.AddDate(0, 0, 1)) {
				label = od.Format("Monday")
			}
			lines = append(lines, " "+dim+label+reset)
		}
		lines = append(lines, c.eventRow(o, now, w, first && !o.allDay))
		first = first && o.allDay
	}
	return fill(lines, h, errLine(err, w))
}

// eventRow draws "  10:00  Standup              in 12m  join".
func (c *calendar) eventRow(o occurrence, now time.Time, w int, next bool) string {
	clock := func(t time.Time) string {
		t = t.In(time.Local)
		if c.format == "12h" {
			return t.Format("3:04pm")
		}
		return t.Format("15:04")
	}
	var when, color string
	switch {
	case !o.start.After(now): // happening now
		color = green + bold
		when = green + "now · " + dur(o.end.Sub(now).Milliseconds()+59999) + " left" + reset
	case o.start.Sub(now) <= time.Hour:
		color = yellow + bold
		when = yellow + "in " + dur(o.start.Sub(now).Milliseconds()+59999) + reset
	case next:
		color = bold
	}
	right := when
	if o.link != "" {
		if right != "" {
			right += "  "
		}
		right += cyan + "join ↗" + reset
	}
	at := clock(o.start)
	if o.allDay {
		at, color, right = "all day", magenta, ""
	}
	left := fmt.Sprintf("  %s%-7s%s ", dim, at, reset)
	room := w - width(left) - width(right) - 2
	if room < 8 {
		right = ""
		room = w - width(left) - 1
	}
	name := o.summary
	if name == "" {
		name = "(busy)"
	}
	row := left + color + truncate(name, max(0, room)) + reset
	if right != "" {
		row += strings.Repeat(" ", max(1, w-width(row)-width(right)-1)) + right
	}
	return link(o.link, row)
}

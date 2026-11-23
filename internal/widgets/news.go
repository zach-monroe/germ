package widgets

import (
	"context"
	"encoding/xml"
	"fmt"
	"html"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// news shows headlines from RSS/Atom feeds, with a summary of the top
// story, rotating through pages. Options: feeds (comma-separated URLs).
type news struct {
	mu    sync.Mutex
	feeds []string
	items []story
	start time.Time
}

type story struct {
	title, summary, source, link string
	when                         time.Time
}

const defaultFeeds = "https://hnrss.org/frontpage,https://feeds.bbci.co.uk/news/world/rss.xml"

func newNews() Widget {
	var feeds []string
	for _, f := range strings.Split(Option("feeds", defaultFeeds), ",") {
		if f = strings.TrimSpace(f); f != "" {
			feeds = append(feeds, f)
		}
	}
	return &news{feeds: feeds, start: time.Now()}
}

func (n *news) Every() time.Duration { return 15 * time.Minute }
func (n *news) Tick() time.Duration  { return 20 * time.Second } // page rotation

func (n *news) Update(ctx context.Context) error {
	var all []story
	var errs []string
	for _, f := range n.feeds {
		items, err := fetchFeed(ctx, f)
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}
		all = append(all, items...)
	}
	// Interleave sources so one busy feed doesn't crowd out the rest.
	sort.SliceStable(all, func(i, j int) bool { return all[i].when.After(all[j].when) })
	all = interleave(all)
	n.mu.Lock()
	if len(all) > 0 {
		n.items = all
	}
	n.mu.Unlock()
	if len(errs) > 0 && len(all) == 0 {
		return fmt.Errorf("%s", errs[0])
	}
	return nil
}

func interleave(items []story) []story {
	bySrc := map[string][]story{}
	var order []string
	for _, s := range items {
		if _, ok := bySrc[s.source]; !ok {
			order = append(order, s.source)
		}
		bySrc[s.source] = append(bySrc[s.source], s)
	}
	var out []story
	for len(out) < len(items) {
		for _, src := range order {
			if q := bySrc[src]; len(q) > 0 {
				out = append(out, q[0])
				bySrc[src] = q[1:]
			}
		}
	}
	return out
}

type rssDoc struct {
	Channel struct {
		Title string `xml:"title"`
		Items []struct {
			Title string `xml:"title"`
			Link  string `xml:"link"`
			Desc  string `xml:"description"`
			Date  string `xml:"pubDate"`
		} `xml:"item"`
	} `xml:"channel"`
	// Atom
	Title   string `xml:"title"`
	Entries []struct {
		Title string `xml:"title"`
		Link  struct {
			Href string `xml:"href,attr"`
		} `xml:"link"`
		Summary string `xml:"summary"`
		Updated string `xml:"updated"`
	} `xml:"entry"`
}

func fetchFeed(ctx context.Context, url string) ([]story, error) {
	body, err := get(ctx, url)
	if err != nil {
		return nil, err
	}
	var d rssDoc
	if err := xml.Unmarshal(body, &d); err != nil {
		return nil, fmt.Errorf("%s: %w", url, err)
	}
	var out []story
	src := shortSource(d.Channel.Title + d.Title)
	for _, it := range d.Channel.Items {
		t, _ := parseDate(it.Date)
		out = append(out, story{title: clean(it.Title), summary: clean(it.Desc), source: src, link: it.Link, when: t})
	}
	for _, e := range d.Entries {
		t, _ := parseDate(e.Updated)
		out = append(out, story{title: clean(e.Title), summary: clean(e.Summary), source: src, link: e.Link.Href, when: t})
	}
	return out, nil
}

func shortSource(t string) string {
	t = strings.TrimSpace(t)
	for _, sep := range []string{" - ", ": ", " | "} {
		if i := strings.Index(t, sep); i > 0 {
			t = t[:i]
		}
	}
	return truncate(t, 18)
}

var tagRE = regexp.MustCompile(`<[^>]*>`)

func clean(s string) string {
	s = tagRE.ReplaceAllString(s, " ")
	s = html.UnescapeString(s)
	s = strings.Join(strings.Fields(s), " ")
	// Feeds like hnrss put only links/comments in the description.
	if strings.HasPrefix(s, "Article URL:") {
		return ""
	}
	return s
}

func parseDate(s string) (time.Time, error) {
	for _, layout := range []string{time.RFC1123Z, time.RFC1123, time.RFC3339, "Mon, 2 Jan 2006 15:04:05 -0700", "Mon, 2 Jan 2006 15:04:05 MST"} {
		if t, err := time.Parse(layout, strings.TrimSpace(s)); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("bad date %q", s)
}

func ago(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := time.Since(t)
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

func (n *news) Draw(w, h int, err error) []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	if len(n.items) == 0 {
		lines := []string{title(w, "▤", "news", ""), ""}
		if err == nil {
			lines = append(lines, center(dim+"loading…"+reset, w))
		}
		return fill(lines, h, errLine(err, w))
	}
	// Each page: a featured story with its summary, then headlines.
	avail := h - 1
	perPage := max(1, avail-4)
	pages := max(1, (len(n.items)+perPage-1)/perPage)
	page := int(time.Since(n.start)/(20*time.Second)) % pages
	items := n.items[page*perPage:]
	lines := []string{title(w, "▤", "news", fmt.Sprintf("%d/%d", page+1, pages))}

	top := items[0]
	meta := dim + " " + top.source + " · " + ago(top.when) + reset
	for i, l := range wrap(top.title, w-2, 2) {
		lines = append(lines, " "+link(top.link, bold+bright+l+reset))
		_ = i
	}
	if top.summary != "" && avail-len(lines) > 6 {
		for _, l := range wrap(top.summary, w-2, 3) {
			lines = append(lines, " "+white+l+reset)
		}
	}
	lines = append(lines, meta, "")
	colors := []string{cyan, magenta, green, yellow}
	srcColor := map[string]string{}
	for _, s := range items[1:] {
		if len(lines) >= avail+1 {
			break
		}
		c, ok := srcColor[s.source]
		if !ok {
			c = colors[len(srcColor)%len(colors)]
			srcColor[s.source] = c
		}
		tag := ago(s.when)
		room := w - 4 - len(tag)
		lines = append(lines, " "+c+"•"+reset+" "+link(s.link, fmt.Sprintf("%-*s", room, truncate(s.title, room)))+" "+dim+tag+reset)
	}
	return fill(lines, h, errLine(err, w))
}

package widgets

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// prs shows your open pull requests with their checks, and the ones
// waiting for your review, through the GitHub CLI (`gh`, so no token in
// germ's config). Options: filter = "org:acme" (added to both searches).
type prs struct {
	filter string
	gh     func(ctx context.Context, args ...string) ([]byte, error) // for tests

	mu      sync.Mutex
	mine    []pr
	review  []pr
	updated time.Time
}

type pr struct {
	Number   int    `json:"number"`
	Title    string `json:"title"`
	URL      string `json:"url"`
	IsDraft  bool   `json:"isDraft"`
	Updated  string `json:"updatedAt"`
	Decision string `json:"reviewDecision"` // APPROVED, CHANGES_REQUESTED, REVIEW_REQUIRED
	Repo     struct {
		Name string `json:"name"`
	} `json:"repository"`
	Author struct {
		Login string `json:"login"`
	} `json:"author"`
	Commits struct {
		Nodes []struct {
			Commit struct {
				Rollup *struct {
					State string `json:"state"` // SUCCESS, FAILURE, ERROR, PENDING, EXPECTED
				} `json:"statusCheckRollup"`
			} `json:"commit"`
		} `json:"nodes"`
	} `json:"commits"`
}

// checks is the PR's combined check state, or "" if it has none.
func (p pr) checks() string {
	if n := p.Commits.Nodes; len(n) > 0 && n[0].Commit.Rollup != nil {
		return n[0].Commit.Rollup.State
	}
	return ""
}

const prFields = `... on PullRequest { number title url isDraft updatedAt reviewDecision
  repository { name } author { login }
  commits(last: 1) { nodes { commit { statusCheckRollup { state } } } } }`

const prQuery = `query($mine: String!, $review: String!) {
  mine: search(query: $mine, type: ISSUE, first: 20) { nodes { ` + prFields + ` } }
  review: search(query: $review, type: ISSUE, first: 20) { nodes { ` + prFields + ` } }
}`

func newPRs(o Options) Widget {
	return &prs{filter: o.Get("filter", ""), gh: runGH}
}

func runGH(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.Env = append(cmd.Environ(), "GH_PROMPT_DISABLED=1", "NO_COLOR=1")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if errors.Is(err, exec.ErrNotFound) {
		return nil, errors.New("needs the GitHub CLI: cli.github.com")
	}
	if err != nil && ctx.Err() != nil {
		return nil, errors.New("gh didn't answer in time")
	}
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		low := strings.ToLower(msg)
		if strings.Contains(low, "auth login") || strings.Contains(low, "401") || strings.Contains(low, "bad credentials") {
			return nil, errors.New("gh isn't logged in: run gh auth login")
		}
		if msg == "" {
			msg = err.Error()
		}
		if i := strings.IndexByte(msg, '\n'); i > 0 {
			msg = msg[:i]
		}
		return nil, errors.New("gh: " + msg)
	}
	return out, nil
}

func (p *prs) Every() time.Duration { return 2 * time.Minute }
func (p *prs) Tick() time.Duration  { return time.Minute } // ages

func (p *prs) Update(ctx context.Context) error {
	base := "is:pr is:open archived:false"
	if p.filter != "" {
		base += " " + p.filter
	}
	out, err := p.gh(ctx, "api", "graphql",
		"-f", "query="+prQuery,
		"-f", "mine="+base+" author:@me sort:updated-desc",
		"-f", "review="+base+" review-requested:@me sort:updated-desc")
	if err != nil {
		return err
	}
	var resp struct {
		Data struct {
			Mine   struct{ Nodes []pr } `json:"mine"`
			Review struct{ Nodes []pr } `json:"review"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		return fmt.Errorf("gh: %v", err)
	}
	if len(resp.Errors) > 0 {
		return errors.New("github: " + resp.Errors[0].Message)
	}
	p.mu.Lock()
	p.mine, p.review, p.updated = resp.Data.Mine.Nodes, resp.Data.Review.Nodes, time.Now()
	p.mu.Unlock()
	return nil
}

func (p *prs) Draw(w, h int, err error) []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	info := ""
	if !p.updated.IsZero() {
		info = fmt.Sprintf("%d yours · %d to review", len(p.mine), len(p.review))
		if w < 40 {
			info = fmt.Sprintf("%d · %d", len(p.mine), len(p.review))
		}
	}
	lines := []string{title(w, "⑂", "pull requests", info)}
	if p.updated.IsZero() {
		if err == nil {
			lines = append(lines, "", center(dim+"loading…"+reset, w))
		}
		return fill(lines, h, errLine(err, w))
	}
	avail := h - 1 - boolInt(err != nil)
	// Split the rows between the two lists, giving either one's spare
	// rows to the other.
	need := func(n int) int {
		if n == 0 {
			return 0
		}
		return n + 1 // heading
	}
	mineRows, reviewRows := need(len(p.mine)), need(len(p.review))
	if mineRows+reviewRows > avail {
		half := avail / 2
		switch {
		case mineRows <= half:
			reviewRows = avail - mineRows
		case reviewRows <= avail-half:
			mineRows = avail - reviewRows
		default:
			mineRows, reviewRows = half, avail-half
		}
	}
	section := func(name string, list []pr, rows int, review bool) {
		if rows < 2 {
			return
		}
		lines = append(lines, " "+dim+name+reset)
		for i, x := range list {
			if i >= rows-1 {
				break
			}
			lines = append(lines, prRow(x, w, review))
		}
	}
	if len(p.mine) == 0 && len(p.review) == 0 {
		lines = append(lines, "", dim+"  No open pull requests."+reset)
	}
	section("yours", p.mine, mineRows, false)
	section("review requested", p.review, reviewRows, true)
	return fill(lines, h, errLine(err, w))
}

// prRow draws "  ✓ #123 Fix the login flow        germ · approved  2h".
func prRow(x pr, w int, review bool) string {
	mark := dim + "·" + reset
	switch x.checks() {
	case "SUCCESS":
		mark = green + "✓" + reset
	case "FAILURE", "ERROR":
		mark = red + "✗" + reset
	case "PENDING", "EXPECTED":
		mark = yellow + "●" + reset
	}
	var tags []string
	if x.IsDraft {
		tags = append(tags, dim+"draft"+reset)
	}
	switch x.Decision {
	case "APPROVED":
		tags = append(tags, green+"approved"+reset)
	case "CHANGES_REQUESTED":
		tags = append(tags, red+"changes"+reset)
	}
	right := dim + x.Repo.Name + reset
	if review && x.Author.Login != "" {
		right = dim + x.Author.Login + reset + " " + right
	}
	for _, t := range tags {
		right += " " + t
	}
	if t, err := time.Parse(time.RFC3339, x.Updated); err == nil {
		right += "  " + dim + fmt.Sprintf("%4s", age(time.Since(t))) + reset
	}
	num := fmt.Sprintf("#%d", x.Number)
	left := "  " + mark + " " + bold + num + reset + " "
	room := w - width(left) - width(right) - 2
	if room < 8 { // narrow: drop the details
		right = ""
		room = w - width(left) - 1
	}
	text := truncate(x.Title, max(0, room))
	if x.IsDraft {
		text = dim + text + reset
	}
	row := left + text
	if right != "" {
		row += strings.Repeat(" ", max(1, w-width(row)-width(right)-1)) + right
	}
	return link(x.URL, row)
}

// age is a short relative time: 5m, 3h, 2d, 6w.
func age(d time.Duration) string {
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm", max(0, int(d.Minutes())))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	case d < 14*24*time.Hour:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
	return fmt.Sprintf("%dw", int(d.Hours()/24/7))
}

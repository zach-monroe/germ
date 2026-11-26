package widgets

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func prJSON(updated string) string {
	return `{"data":{"mine":{"nodes":[
  {"number":12,"title":"Fix the login flow","url":"https://github.com/me/app/pull/12","isDraft":false,
   "updatedAt":"` + updated + `","reviewDecision":"APPROVED","repository":{"name":"app"},"author":{"login":"me"},
   "commits":{"nodes":[{"commit":{"statusCheckRollup":{"state":"SUCCESS"}}}]}},
  {"number":9,"title":"WIP: new parser","url":"https://github.com/me/app/pull/9","isDraft":true,
   "updatedAt":"` + updated + `","reviewDecision":null,"repository":{"name":"app"},"author":{"login":"me"},
   "commits":{"nodes":[{"commit":{"statusCheckRollup":{"state":"FAILURE"}}}]}}]},
 "review":{"nodes":[
  {"number":301,"title":"Add caching","url":"https://github.com/acme/api/pull/301","isDraft":false,
   "updatedAt":"` + updated + `","reviewDecision":"REVIEW_REQUIRED","repository":{"name":"api"},"author":{"login":"alice"},
   "commits":{"nodes":[{"commit":{"statusCheckRollup":null}}]}}]}}}`
}

func TestPRs(t *testing.T) {
	var gotArgs []string
	updated := time.Now().Add(-3 * time.Hour).UTC().Format(time.RFC3339)
	p := &prs{filter: "org:acme", gh: func(_ context.Context, args ...string) ([]byte, error) {
		gotArgs = args
		return []byte(prJSON(updated)), nil
	}}
	if err := p.Update(context.Background()); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(gotArgs, " ")
	for _, want := range []string{"author:@me", "review-requested:@me", "org:acme", "is:open"} {
		if !strings.Contains(joined, want) {
			t.Errorf("gh args lack %q: %s", want, joined)
		}
	}
	if len(p.mine) != 2 || len(p.review) != 1 || p.mine[0].checks() != "SUCCESS" || p.review[0].checks() != "" {
		t.Fatalf("parsed mine=%+v review=%+v", p.mine, p.review)
	}
	out := strings.Join(p.Draw(70, 10, nil), "\n")
	for _, want := range []string{"2 yours · 1 to review", "✓", "#12", "Fix the login flow", "approved", "3h",
		"✗", "draft", "review requested", "alice", "https://github.com/acme/api/pull/301"} {
		if !strings.Contains(out, want) {
			t.Errorf("draw lacks %q:\n%s", want, out)
		}
	}
	for _, sz := range [][2]int{{70, 10}, {30, 4}, {20, 3}, {120, 40}} {
		lines := p.Draw(sz[0], sz[1], nil)
		if len(lines) > sz[1] {
			t.Errorf("%v: %d lines", sz, len(lines))
		}
		for _, l := range lines {
			if width(l) > sz[0] {
				t.Errorf("%v: too wide (%d): %q", sz, width(l), l)
			}
		}
	}
	// Short on room, both lists still show.
	small := strings.Join(p.Draw(70, 5, nil), "\n")
	if !strings.Contains(small, "#12") || !strings.Contains(small, "#301") {
		t.Errorf("5 rows:\n%s", small)
	}
}

func TestPRsErrors(t *testing.T) {
	p := &prs{gh: func(context.Context, ...string) ([]byte, error) {
		return nil, errors.New("gh isn't logged in: run gh auth login")
	}}
	err := p.Update(context.Background())
	if out := strings.Join(p.Draw(60, 5, err), "\n"); !strings.Contains(out, "gh auth login") {
		t.Errorf("error not shown:\n%s", out)
	}
	p.gh = func(context.Context, ...string) ([]byte, error) {
		return []byte(`{"errors":[{"message":"Something broke"}]}`), nil
	}
	if err := p.Update(context.Background()); err == nil || !strings.Contains(err.Error(), "Something broke") {
		t.Errorf("graphql error = %v", err)
	}
}

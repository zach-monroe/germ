package widgets

import (
	"context"
	"strings"
	"testing"

	"github.com/zach-monroe/germ/internal/control"
	"github.com/zach-monroe/germ/internal/share"
)

func TestCoworking(t *testing.T) {
	st := control.ShareStatus{
		Host: true, Session: "germ", Via: "through a Cloudflare quick tunnel",
		People: []share.Participant{
			{ID: 2, Name: "alice@laptop", Web: true, Typing: true},
			{ID: 0, Name: "zach@desk", Host: true},
			{ID: 3, Name: "bob@box"},
		},
		Tabs: []share.Tab{
			{Window: "@1", Index: 1, Name: "zsh"},
			{Window: "@2", Index: 2, Name: "feature", Owner: 2, Helm: 2},
		},
		Current:  "@1",
		Requests: []share.HelmRequest{{ID: 1, From: 3, Name: "bob@box", Window: "@2"}},
		Joins:    []share.JoinRequest{{ID: 9, Name: "carol@home"}},
		Unread:   2,
		Last:     &share.ChatMsg{Name: "alice@laptop", Text: "pushed the\nfix"},
	}
	c := &coworking{status: func() []control.ShareStatus { return []control.ShareStatus{st} }}
	c.Update(context.Background())
	lines := c.Draw(72, 14, nil)
	out := strings.Join(lines, "\n")
	for _, want := range []string{"2 waiting on you", "sharing", "germ", "Cloudflare",
		"zach", "you", "host", "on 1 zsh", "alice", "browser", "✎ typing", "⎈ 2 feature",
		"carol wants to join", "bob asks for the helm of 2 feature", "✉ 2", "alice:", "pushed the fix"} {
		if !strings.Contains(out, want) {
			t.Errorf("draw lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "@laptop") {
		t.Error("machine names shown")
	}
	// The host is listed first.
	for i, l := range lines {
		if strings.Contains(l, "alice") && !strings.Contains(strings.Join(lines[:i], ""), "zach") {
			t.Error("host not first")
			break
		}
	}
	for _, sz := range [][2]int{{72, 14}, {40, 6}, {24, 3}} {
		ls := c.Draw(sz[0], sz[1], nil)
		if len(ls) > sz[1] {
			t.Errorf("%v: %d lines", sz, len(ls))
		}
		for _, l := range ls {
			if width(l) > sz[0] {
				t.Errorf("%v: too wide (%d): %q", sz, width(l), l)
			}
		}
	}

	// A guest's view: joined someone's session.
	guest := control.ShareStatus{Session: "work", You: 4, People: []share.Participant{
		{ID: 0, Name: "dana@srv", Host: true}, {ID: 4, Name: "zach@desk"}}}
	c.status = func() []control.ShareStatus { return []control.ShareStatus{guest} }
	c.Update(context.Background())
	out = strings.Join(c.Draw(72, 8, nil), "\n")
	if !strings.Contains(out, "joined") || !strings.Contains(out, "dana") || strings.Contains(out, "wants to join") {
		t.Errorf("guest view:\n%s", out)
	}

	c.status = func() []control.ShareStatus { return nil }
	c.Update(context.Background())
	if out := strings.Join(c.Draw(60, 6, nil), "\n"); !strings.Contains(out, "Not sharing") {
		t.Errorf("idle:\n%s", out)
	}
}

func TestTruncateStyled(t *testing.T) {
	s := bold + "héllo" + reset + " world"
	got := truncateStyled(s, 4)
	if width(got) != 4 || !strings.HasPrefix(got, bold+"hél") {
		t.Errorf("truncateStyled = %q (width %d)", got, width(got))
	}
	if truncateStyled("hi", 5) != "hi" {
		t.Error("short string changed")
	}
}

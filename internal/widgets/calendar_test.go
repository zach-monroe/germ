package widgets

import (
	"context"
	"strings"
	"testing"
	"time"
)

func mustLoc(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Skip("no tzdata:", err)
	}
	return loc
}

func starts(ts []time.Time) []string {
	var out []string
	for _, t := range ts {
		out = append(out, t.Format("2006-01-02 15:04 Mon"))
	}
	return out
}

func TestExpandRRule(t *testing.T) {
	ny := mustLoc(t, "America/New_York")
	start := time.Date(2026, 3, 2, 10, 0, 0, 0, ny) // a Monday, a week before DST starts
	to := time.Date(2026, 3, 20, 0, 0, 0, 0, ny)
	for _, c := range []struct {
		rule string
		want []string
	}{
		{"FREQ=WEEKLY;BYDAY=MO,WE", []string{"2026-03-02 10:00 Mon", "2026-03-04 10:00 Wed", "2026-03-09 10:00 Mon",
			"2026-03-11 10:00 Wed", "2026-03-16 10:00 Mon", "2026-03-18 10:00 Wed"}},
		{"FREQ=WEEKLY;INTERVAL=2", []string{"2026-03-02 10:00 Mon", "2026-03-16 10:00 Mon"}},
		{"FREQ=DAILY;COUNT=3", []string{"2026-03-02 10:00 Mon", "2026-03-03 10:00 Tue", "2026-03-04 10:00 Wed"}},
		{"FREQ=DAILY;UNTIL=20260304", []string{"2026-03-02 10:00 Mon", "2026-03-03 10:00 Tue", "2026-03-04 10:00 Wed"}},
		{"FREQ=DAILY;BYDAY=SA,SU", []string{"2026-03-07 10:00 Sat", "2026-03-08 10:00 Sun", "2026-03-14 10:00 Sat", "2026-03-15 10:00 Sun"}},
	} {
		got := starts(expandRRule(start, c.rule, to))
		if strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Errorf("%s:\n got %q\nwant %q", c.rule, got, c.want)
		}
	}
	// 10:00 stays 10:00 across DST (Mar 8): the UTC offset changes.
	got := expandRRule(start, "FREQ=WEEKLY", to)
	if _, off0 := got[0].Zone(); func() int { _, o := got[1].Zone(); return o }() == off0 {
		t.Error("UTC offset didn't change across DST")
	}

	mstart := time.Date(2026, 1, 13, 9, 0, 0, 0, time.UTC) // 2nd Tuesday
	mto := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		rule string
		want []string
	}{
		{"FREQ=MONTHLY;BYDAY=2TU", []string{"2026-01-13 09:00 Tue", "2026-02-10 09:00 Tue", "2026-03-10 09:00 Tue",
			"2026-04-14 09:00 Tue", "2026-05-12 09:00 Tue"}},
		{"FREQ=MONTHLY;BYDAY=-1FR;COUNT=3", []string{"2026-01-30 09:00 Fri", "2026-02-27 09:00 Fri", "2026-03-27 09:00 Fri"}},
		{"FREQ=MONTHLY;BYMONTHDAY=31", []string{"2026-01-31 09:00 Sat", "2026-03-31 09:00 Tue", "2026-05-31 09:00 Sun"}},
		{"FREQ=MONTHLY", []string{"2026-01-13 09:00 Tue", "2026-02-13 09:00 Fri", "2026-03-13 09:00 Fri",
			"2026-04-13 09:00 Mon", "2026-05-13 09:00 Wed"}},
		{"FREQ=YEARLY;BYMONTH=3,5;BYMONTHDAY=1", []string{"2026-03-01 09:00 Sun", "2026-05-01 09:00 Fri"}},
	} {
		got := starts(expandRRule(mstart, c.rule, mto))
		if strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Errorf("%s:\n got %q\nwant %q", c.rule, got, c.want)
		}
	}
}

const testICS = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\n" +
	// A weekly standup with a skipped week, a moved one, and an alarm.
	"BEGIN:VEVENT\r\nUID:standup\r\nSUMMARY:Standup\r\n" +
	"DTSTART;TZID=Eastern Standard Time:20261102T100000\r\nDTEND;TZID=Eastern Standard Time:20261102T101500\r\n" +
	"RRULE:FREQ=WEEKLY;BYDAY=MO,TU,WE,TH,FR\r\nEXDATE;TZID=Eastern Standard Time:20261126T100000\r\n" +
	"LOCATION:https://meet.google.com/abc-defg-hij\r\n" +
	"BEGIN:VALARM\r\nACTION:DISPLAY\r\nDESCRIPTION:not the event\r\nEND:VALARM\r\nEND:VEVENT\r\n" +
	"BEGIN:VEVENT\r\nUID:standup\r\nRECURRENCE-ID;TZID=Eastern Standard Time:20261127T100000\r\n" +
	"SUMMARY:Standup (moved)\r\nDTSTART;TZID=America/New_York:20261127T140000\r\nDURATION:PT30M\r\nEND:VEVENT\r\n" +
	// A one-off with a folded description holding a Zoom link.
	"BEGIN:VEVENT\r\nUID:one\r\nSUMMARY:1:1 with Sam\\, and notes\r\n" +
	"DTSTART:20261125T190000Z\r\nDTEND:20261125T193000Z\r\n" +
	"DESCRIPTION:Join: https://acme.zoom.us/j/12345\r\n ?pwd=x\\nagenda\r\nEND:VEVENT\r\n" +
	// A holiday, and a cancelled meeting.
	"BEGIN:VEVENT\r\nUID:hol\r\nSUMMARY:Thanksgiving\r\nDTSTART;VALUE=DATE:20261126\r\nDTEND;VALUE=DATE:20261127\r\nEND:VEVENT\r\n" +
	"BEGIN:VEVENT\r\nUID:gone\r\nSUMMARY:Cancelled thing\r\nSTATUS:CANCELLED\r\nDTSTART:20261125T200000Z\r\nEND:VEVENT\r\n" +
	"END:VCALENDAR\r\n"

func TestCalendarFeed(t *testing.T) {
	ny := mustLoc(t, "America/New_York")
	old := time.Local // all-day events start at local midnight
	time.Local = ny
	defer func() { time.Local = old }()
	evs, err := parseICal(testICS)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 5 {
		t.Fatalf("%d events", len(evs))
	}
	from := time.Date(2026, 11, 25, 0, 0, 0, 0, ny)
	occ := occurrences(evs, from, from.AddDate(0, 0, 3))
	var got []string
	for _, o := range occ {
		got = append(got, o.start.In(ny).Format("Mon 15:04")+" "+o.summary)
	}
	want := []string{"Wed 10:00 Standup", "Wed 14:00 1:1 with Sam, and notes", "Thu 00:00 Thanksgiving", "Fri 14:00 Standup (moved)"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("occurrences:\n got %q\nwant %q", got, want)
	}
	if occ[0].link != "https://meet.google.com/abc-defg-hij" || occ[1].link != "https://acme.zoom.us/j/12345?pwd=x" {
		t.Errorf("links %q, %q", occ[0].link, occ[1].link)
	}
	if occ[3].end.Sub(occ[3].start) != 30*time.Minute {
		t.Errorf("moved standup lasts %v", occ[3].end.Sub(occ[3].start))
	}
}

func TestCalendarDraw(t *testing.T) {
	ny := mustLoc(t, "America/New_York")
	old := time.Local
	time.Local = ny
	defer func() { time.Local = old }()
	now := time.Date(2026, 11, 25, 9, 50, 0, 0, ny)
	fetched := 0
	c := &calendar{urls: []string{"https://calendar.example.com/private-secret/basic.ics"}, days: 3, format: "24h",
		now:   func() time.Time { return now },
		fetch: func(context.Context, string) ([]byte, error) { fetched++; return []byte(testICS), nil }}
	if err := c.Update(context.Background()); err != nil {
		t.Fatal(err)
	}
	out := strings.Join(c.Draw(64, 12, nil), "\n")
	for _, want := range []string{"Wed Nov 25", "10:00", "Standup", "in 10m", "join ↗", "https://meet.google.com/abc-defg-hij",
		"14:00", "1:1 with Sam", "Friday", "Standup (moved)"} {
		if !strings.Contains(out, want) {
			t.Errorf("draw lacks %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "all day") || !strings.Contains(out, "Thanksgiving") {
		t.Errorf("tomorrow's holiday not listed:\n%s", out)
	}
	if strings.Contains(out, "Cancelled") {
		t.Error("cancelled event shown")
	}
	// During the standup: "now".
	now = time.Date(2026, 11, 25, 10, 5, 0, 0, ny)
	if out := strings.Join(c.Draw(64, 12, nil), "\n"); !strings.Contains(out, "now · 10m left") {
		t.Errorf("during:\n%s", out)
	}
	// Tomorrow's all-day event shows on the day.
	now = time.Date(2026, 11, 26, 8, 0, 0, 0, ny)
	out = strings.Join(c.Draw(64, 12, nil), "\n")
	if !strings.Contains(out, "◆ Thanksgiving") || !strings.Contains(out, "tomorrow") {
		t.Errorf("Thursday (holiday, standup tomorrow):\n%s", out)
	}
	for _, sz := range [][2]int{{64, 12}, {30, 4}, {20, 3}} {
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
	// A failing feed: the error names the host, never the secret path.
	c.fetch = func(context.Context, string) ([]byte, error) { return nil, context.DeadlineExceeded }
	err := c.Update(context.Background())
	if err == nil || strings.Contains(err.Error(), "secret") || !strings.Contains(err.Error(), "calendar.example.com") {
		t.Errorf("error = %v", err)
	}
	if out := strings.Join(c.Draw(64, 12, err), "\n"); !strings.Contains(out, "Standup") {
		t.Error("last good events dropped on error")
	}
}

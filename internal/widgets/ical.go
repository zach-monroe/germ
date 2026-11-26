package widgets

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// A small iCalendar (RFC 5545) reader: enough for the feeds Google,
// Outlook, Fastmail and friends publish. It understands VEVENTs with
// DTSTART/DTEND/DURATION (dates, UTC, floating and TZID times), RRULE
// (DAILY, WEEKLY, MONTHLY, YEARLY with INTERVAL, COUNT, UNTIL, BYDAY,
// BYMONTHDAY, BYMONTH), EXDATE, and RECURRENCE-ID overrides and
// cancellations.

type icalEvent struct {
	uid, summary, location, description, url, status string
	start, end                                       time.Time
	allDay                                           bool
	rrule                                            string
	exdates                                          map[int64]bool // Unix seconds
	recurrenceID                                     time.Time      // set on an override of one occurrence
}

// occurrence is one event instance in a time window.
type occurrence struct {
	summary, location, link string
	start, end              time.Time
	allDay                  bool
}

// parseICal reads the events of a calendar.
func parseICal(data string) ([]icalEvent, error) {
	// Unfold: a line starting with a space or tab continues the last.
	data = strings.ReplaceAll(data, "\r\n", "\n")
	var lines []string
	for _, l := range strings.Split(data, "\n") {
		if len(l) > 0 && (l[0] == ' ' || l[0] == '\t') && len(lines) > 0 {
			lines[len(lines)-1] += l[1:]
			continue
		}
		lines = append(lines, l)
	}
	if len(lines) == 0 || !strings.HasPrefix(strings.ToUpper(strings.TrimSpace(lines[0])), "BEGIN:VCALENDAR") {
		return nil, fmt.Errorf("not an iCalendar file")
	}
	var events []icalEvent
	var cur *icalEvent
	depth := 0 // nested components inside a VEVENT (VALARM)
	var duration time.Duration
	hasDuration := false
	for _, l := range lines {
		name, params, value := icalProp(l)
		switch {
		case name == "BEGIN" && strings.EqualFold(value, "VEVENT"):
			cur = &icalEvent{exdates: map[int64]bool{}}
			hasDuration, depth = false, 0
			continue
		case name == "BEGIN" && cur != nil:
			depth++
			continue
		case name == "END" && cur != nil && depth > 0:
			depth--
			continue
		case name == "END" && strings.EqualFold(value, "VEVENT") && cur != nil:
			if cur.end.IsZero() {
				switch {
				case hasDuration:
					cur.end = cur.start.Add(duration)
				case cur.allDay:
					cur.end = cur.start.AddDate(0, 0, 1)
				default:
					cur.end = cur.start
				}
			}
			if !cur.start.IsZero() {
				events = append(events, *cur)
			}
			cur = nil
			continue
		}
		if cur == nil || depth > 0 {
			continue
		}
		switch name {
		case "UID":
			cur.uid = value
		case "SUMMARY":
			cur.summary = icalText(value)
		case "LOCATION":
			cur.location = icalText(value)
		case "DESCRIPTION":
			cur.description = icalText(value)
		case "URL":
			cur.url = value
		case "STATUS":
			cur.status = strings.ToUpper(value)
		case "DTSTART":
			cur.start, cur.allDay = icalTime(value, params)
		case "DTEND":
			cur.end, _ = icalTime(value, params)
		case "DURATION":
			duration, hasDuration = icalDuration(value)
		case "RRULE":
			cur.rrule = value
		case "EXDATE":
			for _, v := range strings.Split(value, ",") {
				if t, _ := icalTime(v, params); !t.IsZero() {
					cur.exdates[t.Unix()] = true
				}
			}
		case "RECURRENCE-ID":
			cur.recurrenceID, _ = icalTime(value, params)
		}
	}
	return events, nil
}

// icalProp splits "NAME;P=V;Q=W:value".
func icalProp(l string) (name string, params map[string]string, value string) {
	l = strings.TrimRight(l, "\r")
	// The value starts at the first colon outside a quoted parameter.
	inQuote, colon := false, -1
	for i := 0; i < len(l); i++ {
		if l[i] == '"' {
			inQuote = !inQuote
		} else if l[i] == ':' && !inQuote {
			colon = i
			break
		}
	}
	if colon < 0 {
		return "", nil, ""
	}
	head, value := l[:colon], l[colon+1:]
	parts := strings.Split(head, ";")
	name = strings.ToUpper(parts[0])
	params = map[string]string{}
	for _, p := range parts[1:] {
		if k, v, ok := strings.Cut(p, "="); ok {
			params[strings.ToUpper(k)] = strings.Trim(v, `"`)
		}
	}
	return name, params, value
}

func icalText(v string) string {
	r := strings.NewReplacer(`\n`, "\n", `\N`, "\n", `\,`, ",", `\;`, ";", `\\`, `\`)
	return r.Replace(v)
}

// windowsZones maps the zone names Outlook writes to IANA ones.
var windowsZones = map[string]string{
	"Pacific Standard Time": "America/Los_Angeles", "Mountain Standard Time": "America/Denver",
	"US Mountain Standard Time": "America/Phoenix", "Central Standard Time": "America/Chicago",
	"Eastern Standard Time": "America/New_York", "Alaskan Standard Time": "America/Anchorage",
	"Hawaiian Standard Time": "Pacific/Honolulu", "Atlantic Standard Time": "America/Halifax",
	"GMT Standard Time": "Europe/London", "W. Europe Standard Time": "Europe/Berlin",
	"Romance Standard Time": "Europe/Paris", "Central Europe Standard Time": "Europe/Budapest",
	"Central European Standard Time": "Europe/Warsaw", "E. Europe Standard Time": "Europe/Chisinau",
	"FLE Standard Time": "Europe/Kiev", "GTB Standard Time": "Europe/Bucharest",
	"Russian Standard Time": "Europe/Moscow", "India Standard Time": "Asia/Kolkata",
	"China Standard Time": "Asia/Shanghai", "Tokyo Standard Time": "Asia/Tokyo",
	"Korea Standard Time": "Asia/Seoul", "Singapore Standard Time": "Asia/Singapore",
	"AUS Eastern Standard Time": "Australia/Sydney", "New Zealand Standard Time": "Pacific/Auckland",
	"E. South America Standard Time": "America/Sao_Paulo", "UTC": "UTC", "Coordinated Universal Time": "UTC",
}

func icalZone(tzid string) *time.Location {
	if tzid == "" {
		return time.Local
	}
	if iana, ok := windowsZones[tzid]; ok {
		tzid = iana
	}
	// Some feeds write "/mozilla.org/.../America/New_York": try the tail.
	for name := tzid; name != ""; {
		if loc, err := time.LoadLocation(name); err == nil {
			return loc
		}
		_, rest, ok := strings.Cut(name, "/")
		if !ok {
			break
		}
		name = rest
	}
	return time.Local
}

// icalTime parses a DATE or DATE-TIME value; allDay for a DATE.
func icalTime(v string, params map[string]string) (time.Time, bool) {
	v = strings.TrimSpace(v)
	loc := icalZone(params["TZID"])
	if params["VALUE"] == "DATE" || len(v) == 8 {
		t, err := time.ParseInLocation("20060102", v, time.Local)
		if err != nil {
			return time.Time{}, false
		}
		return t, true
	}
	if strings.HasSuffix(v, "Z") {
		t, err := time.Parse("20060102T150405Z", v)
		if err != nil {
			return time.Time{}, false
		}
		return t, false
	}
	t, err := time.ParseInLocation("20060102T150405", v, loc)
	if err != nil {
		return time.Time{}, false
	}
	return t, false
}

// icalDuration parses "PT1H30M", "P1D", "-PT15M".
func icalDuration(v string) (time.Duration, bool) {
	neg := strings.HasPrefix(v, "-")
	v = strings.TrimLeft(v, "+-")
	if !strings.HasPrefix(v, "P") {
		return 0, false
	}
	v = v[1:]
	var d time.Duration
	inTime := false
	num := ""
	for _, c := range v {
		switch {
		case c >= '0' && c <= '9':
			num += string(c)
		case c == 'T':
			inTime = true
		default:
			n, _ := strconv.Atoi(num)
			num = ""
			switch {
			case c == 'W':
				d += time.Duration(n) * 7 * 24 * time.Hour
			case c == 'D':
				d += time.Duration(n) * 24 * time.Hour
			case c == 'H' && inTime:
				d += time.Duration(n) * time.Hour
			case c == 'M' && inTime:
				d += time.Duration(n) * time.Minute
			case c == 'S' && inTime:
				d += time.Duration(n) * time.Second
			}
		}
	}
	if neg {
		d = -d
	}
	return d, true
}

// occurrences expands events into the instances overlapping [from, to).
func occurrences(events []icalEvent, from, to time.Time) []occurrence {
	// Overrides of single occurrences, by UID and original start.
	overrides := map[string]map[int64]bool{}
	for _, e := range events {
		if !e.recurrenceID.IsZero() {
			if overrides[e.uid] == nil {
				overrides[e.uid] = map[int64]bool{}
			}
			overrides[e.uid][e.recurrenceID.Unix()] = true
		}
	}
	var out []occurrence
	add := func(e icalEvent, start time.Time) {
		end := start.Add(e.end.Sub(e.start))
		if e.allDay { // keep calendar days across DST changes
			days := int(e.end.Sub(e.start).Hours()/24 + 0.5)
			end = start.AddDate(0, 0, max(1, days))
		}
		if !end.After(from) || !start.Before(to) {
			return
		}
		out = append(out, occurrence{summary: e.summary, location: e.location, link: meetingLink(e),
			start: start, end: end, allDay: e.allDay})
	}
	for _, e := range events {
		if e.status == "CANCELLED" {
			continue
		}
		if e.rrule == "" || !e.recurrenceID.IsZero() {
			add(e, e.start)
			continue
		}
		skip := overrides[e.uid]
		for _, s := range expandRRule(e.start, e.rrule, to) {
			if e.exdates[s.Unix()] || skip[s.Unix()] {
				continue
			}
			add(e, s)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].start.Before(out[j].start) })
	return out
}

var weekdays = map[string]time.Weekday{"SU": time.Sunday, "MO": time.Monday, "TU": time.Tuesday,
	"WE": time.Wednesday, "TH": time.Thursday, "FR": time.Friday, "SA": time.Saturday}

type byDay struct {
	n   int // 0: every; 1: first; -1: last
	day time.Weekday
}

// expandRRule lists the starts of a recurring event up to before `to`.
// Times keep the start's wall clock in its zone (so 10:00 stays 10:00
// across DST).
func expandRRule(start time.Time, rule string, to time.Time) []time.Time {
	r := map[string]string{}
	for _, part := range strings.Split(rule, ";") {
		if k, v, ok := strings.Cut(part, "="); ok {
			r[strings.ToUpper(k)] = strings.ToUpper(v)
		}
	}
	interval := max(1, atoiOr(r["INTERVAL"], 1))
	count := atoiOr(r["COUNT"], 0)
	var until time.Time
	if u := r["UNTIL"]; u != "" {
		until, _ = icalTime(u, map[string]string{})
		if len(u) == 8 { // a date: through the end of that day
			until = until.AddDate(0, 0, 1).Add(-time.Second)
		}
	}
	var days []byDay
	for _, d := range strings.Split(r["BYDAY"], ",") {
		if len(d) < 2 {
			continue
		}
		wd, ok := weekdays[d[len(d)-2:]]
		if !ok {
			continue
		}
		days = append(days, byDay{atoiOr(d[:len(d)-2], 0), wd})
	}
	var monthDays []int
	for _, d := range strings.Split(r["BYMONTHDAY"], ",") {
		if n := atoiOr(d, 0); n != 0 {
			monthDays = append(monthDays, n)
		}
	}
	var months []time.Month
	for _, m := range strings.Split(r["BYMONTH"], ",") {
		if n := atoiOr(m, 0); n >= 1 && n <= 12 {
			months = append(months, time.Month(n))
		}
	}
	loc := start.Location()
	h, mi, s := start.Clock()
	at := func(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, h, mi, s, 0, loc) }

	var out []time.Time
	n := 0
	// emit adds a candidate; false once the rule is done.
	emit := func(t time.Time) bool {
		if t.Before(start) {
			return true
		}
		if (!until.IsZero() && t.After(until)) || !t.Before(to) {
			return false
		}
		if count > 0 && n >= count {
			return false
		}
		n++
		out = append(out, t)
		return true
	}
	monthOK := func(m time.Month) bool {
		if len(months) == 0 {
			return true
		}
		for _, x := range months {
			if x == m {
				return true
			}
		}
		return false
	}
	const maxPeriods = 20000 // a daily rule from 50 years ago
	y, m, d := start.Date()
	switch r["FREQ"] {
	case "DAILY":
		for i := 0; i < maxPeriods; i++ {
			t := at(y, m, d+i*interval)
			if !monthOK(t.Month()) || (len(days) > 0 && !hasWeekday(days, t.Weekday())) {
				if !t.Before(to) {
					return out
				}
				continue
			}
			if !emit(t) {
				return out
			}
		}
	case "WEEKLY":
		if len(days) == 0 {
			days = []byDay{{0, start.Weekday()}}
		}
		// Weeks start on Monday (WKST default).
		weekStart := at(y, m, d-(int(start.Weekday())+6)%7)
		for i := 0; i < maxPeriods; i++ {
			ws := weekStart.AddDate(0, 0, 7*i*interval)
			ws = at(ws.Date())
			if ws.After(to) {
				return out
			}
			for off := 0; off < 7; off++ {
				t := at(ws.Year(), ws.Month(), ws.Day()+off)
				if hasWeekday(days, t.Weekday()) && monthOK(t.Month()) && !emit(t) {
					return out
				}
			}
		}
	case "MONTHLY", "YEARLY":
		step := interval
		if r["FREQ"] == "YEARLY" {
			step = 12 * interval
		}
		for i := 0; i < maxPeriods; i++ {
			first := time.Date(y, m+time.Month(i*step), 1, 0, 0, 0, 0, loc)
			if at(first.Date()).After(to) {
				return out
			}
			var ts []time.Time
			// A yearly rule with BYMONTH runs in each of those months.
			monthsHere := []time.Month{first.Month()}
			if r["FREQ"] == "YEARLY" && len(months) > 0 {
				monthsHere = months
			}
			for _, mon := range monthsHere {
				fy := first.Year()
				switch {
				case len(days) > 0:
					ts = append(ts, monthWeekdays(fy, mon, days, at)...)
				case len(monthDays) > 0:
					for _, md := range monthDays {
						if t, ok := monthDay(fy, mon, md, at); ok {
							ts = append(ts, t)
						}
					}
				default:
					if t, ok := monthDay(fy, mon, d, at); ok {
						ts = append(ts, t)
					}
				}
			}
			sort.Slice(ts, func(a, b int) bool { return ts[a].Before(ts[b]) })
			for _, t := range ts {
				if !emit(t) {
					return out
				}
			}
		}
	default:
		emit(start)
	}
	return out
}

func hasWeekday(days []byDay, wd time.Weekday) bool {
	for _, d := range days {
		if d.day == wd {
			return true
		}
	}
	return false
}

// monthDay is day md of a month (negative: from the end); false if the
// month is too short (the 31st of April).
func monthDay(y int, m time.Month, md int, at func(int, time.Month, int) time.Time) (time.Time, bool) {
	last := time.Date(y, m+1, 0, 0, 0, 0, 0, time.UTC).Day()
	if md < 0 {
		md = last + md + 1
	}
	if md < 1 || md > last {
		return time.Time{}, false
	}
	return at(y, m, md), true
}

// monthWeekdays lists a month's days matching BYDAY ("MO", "2TU", "-1FR").
func monthWeekdays(y int, m time.Month, days []byDay, at func(int, time.Month, int) time.Time) []time.Time {
	last := time.Date(y, m+1, 0, 0, 0, 0, 0, time.UTC).Day()
	var out []time.Time
	for day := 1; day <= last; day++ {
		wd := time.Date(y, m, day, 0, 0, 0, 0, time.UTC).Weekday()
		for _, d := range days {
			if d.day != wd {
				continue
			}
			nth := (day-1)/7 + 1           // 1st, 2nd... of the month
			fromEnd := -((last-day)/7 + 1) // -1: last
			if d.n == 0 || d.n == nth || d.n == fromEnd {
				out = append(out, at(y, m, day))
				break
			}
		}
	}
	return out
}

func atoiOr(s string, def int) int {
	n, err := strconv.Atoi(strings.TrimPrefix(s, "+"))
	if err != nil {
		return def
	}
	return n
}

// meetingLink finds a video call link in an event, else its URL.
func meetingLink(e icalEvent) string {
	for _, text := range []string{e.location, e.url, e.description} {
		for _, f := range strings.FieldsFunc(text, func(r rune) bool {
			return r == ' ' || r == '\n' || r == '<' || r == '>' || r == '"' || r == '(' || r == ')'
		}) {
			if !strings.HasPrefix(f, "https://") {
				continue
			}
			for _, host := range []string{"zoom.us/", "meet.google.com/", "teams.microsoft.com/", "teams.live.com/",
				"whereby.com/", "meet.jit.si/", "webex.com/", "around.co/", "gather.town/"} {
				if strings.Contains(f, host) {
					return strings.TrimRight(f, ".,;")
				}
			}
		}
	}
	if strings.HasPrefix(e.url, "https://") || strings.HasPrefix(e.url, "http://") {
		return e.url
	}
	return ""
}

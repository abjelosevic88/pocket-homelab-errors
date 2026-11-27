package server

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	core "github.com/abjelosevic88/pocket-homelab-core/sdk"
	"github.com/abjelosevic88/pocket-homelab-errors/internal/store"
)

// The three routes behind `capabilities` (links.go): /api/v1/pocket/search,
// /day/{date} and /widget. Each runs as the request's person, sees only their
// projects' issues and reads only: issue titles, projects, counts and times.
// No event, stack, culprit or log line goes out here, and nothing is
// resolved, ignored or changed.

func issueURL(id int64) string { return "/issues/" + itoa(id) }

// timesWord is "1 time", "42 times".
func timesWord(n int) string {
	if n == 1 {
		return "1 time"
	}
	return strconv.Itoa(n) + " times"
}

func countWord(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

// stampTime reads a stored time ("" or unreadable = zero).
func stampTime(v string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, v)
	return t
}

// seenWord says when an issue was last seen: "last seen today",
// "last seen yesterday", "last seen 3 Nov", "last seen 3 Nov 2025".
func seenWord(v string, now time.Time) string {
	t := stampTime(v)
	if t.IsZero() {
		return ""
	}
	t = t.In(now.Location())
	day := func(x time.Time) time.Time { return time.Date(x.Year(), x.Month(), x.Day(), 0, 0, 0, 0, x.Location()) }
	switch d := day(now).Sub(day(t)); {
	case d <= 0:
		return "last seen today"
	case d <= 24*time.Hour+time.Hour: // a day, give or take a clock change
		return "last seen yesterday"
	case t.Year() == now.Year():
		return "last seen " + t.Format("2 Jan")
	}
	return "last seen " + t.Format("2 Jan 2006")
}

// hasWord reports whether a query has a letter or digit to look for.
func hasWord(q string) bool {
	return strings.IndexFunc(q, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }) >= 0
}

// ---------- search ----------

// pocketSearch finds the person's issues by title, culprit and project (the
// issue list's own search), open ones first, then the last seen.
func (s *Server) pocketSearch(r *http.Request, q string, limit int) ([]core.SearchItem, error) {
	if !hasWord(q) {
		return nil, nil
	}
	list, _, err := s.store.ListIssues(r.Context(), actorID(r.Context()), store.IssueFilter{Status: "all", Q: q, Limit: 100})
	if err != nil {
		return nil, err
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].Status == "open" && list[j].Status != "open" })
	now := time.Now()
	out := make([]core.SearchItem, 0, min(len(list), limit))
	for _, is := range list {
		if len(out) == limit {
			break
		}
		out = append(out, core.SearchItem{
			ID: itoa(is.ID), Title: is.Title, Kind: "issue", Date: is.LastSeen, URL: issueURL(is.ID),
			Snippet: joinNonEmpty(" · ", is.ProjectName, statusWord(is.Status), timesWord(is.Times), seenWord(is.LastSeen, now)),
		})
	}
	return out, nil
}

// ---------- day ----------

// pocketDay is one day: the issues first seen that day and the ones that
// came back after they were resolved, each with how many times it happened
// that day. Days are the person's time zone when the caller says one, else
// the box's (as GET /day).
func (s *Server) pocketDay(r *http.Request, date string, tz *time.Location) (*core.Day, error) {
	loc := time.Local
	if tz != nil {
		loc = tz
	}
	from, err := time.ParseInLocation("2006-01-02", date, loc)
	if err != nil {
		return nil, nil
	}
	to := from.AddDate(0, 0, 1)
	ctx, uid := r.Context(), actorID(r.Context())
	list, err := s.store.DayIssues(ctx, uid, from, to)
	if err != nil {
		return nil, err
	}
	a, b := store.Stamp(from), store.Stamp(to)
	clock := func(v string) string {
		if t := stampTime(v); !t.IsZero() {
			return t.In(loc).Format("15:04")
		}
		return ""
	}
	day := &core.Day{Date: date}
	fresh, back := 0, 0
	for _, d := range list {
		it := core.DayItem{ID: itoa(d.ID), Title: d.Title, URL: issueURL(d.ID)}
		if d.Events > 0 {
			it.Sub = joinNonEmpty(" · ", d.ProjectName, timesWord(d.Events))
		} else {
			it.Sub = d.ProjectName
		}
		// an issue that started that day and came back the same day is new
		if d.FirstSeen >= a && d.FirstSeen < b {
			fresh++
			it.Kind, it.Time = "new", clock(d.FirstSeen)
		} else {
			back++
			it.Kind, it.Time = "came back", clock(d.BackAt)
		}
		day.Items = append(day.Items, it)
	}
	if len(day.Items) == 0 {
		return nil, nil
	}
	var parts []string
	if fresh > 0 {
		parts = append(parts, countWord(fresh, "new issue", "new issues"))
		day.Metrics = append(day.Metrics, core.Metric{Label: "New", Value: strconv.Itoa(fresh)})
	}
	if back > 0 {
		parts = append(parts, strconv.Itoa(back)+" came back")
		day.Metrics = append(day.Metrics, core.Metric{Label: "Came back", Value: strconv.Itoa(back)})
	}
	if all, err := s.store.EventsBetween(ctx, uid, from, to); err == nil && all > 0 {
		day.Metrics = append(day.Metrics, core.Metric{Label: "Errors", Value: strconv.Itoa(all)})
	}
	day.Summary = strings.Join(parts, ", ")
	return day, nil
}

// ---------- widget ----------

// pocketWidget is the open issues that happened today (the box's day): new
// ones first, then the busiest; its line says how many are new or came back
// today, and nothing on a quiet day.
func (s *Server) pocketWidget(r *http.Request) (*core.WidgetData, error) {
	ctx, uid := r.Context(), actorID(r.Context())
	now := time.Now()
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	open, fresh, back, err := s.store.OpenSince(ctx, uid, midnight)
	if err != nil {
		return nil, err
	}
	busy, err := s.store.BusyOpenIssues(ctx, uid, midnight, 8)
	if err != nil {
		return nil, err
	}
	if open == 0 && len(busy) == 0 {
		return nil, nil
	}
	out := &core.WidgetData{URL: "/", Metrics: []core.Metric{
		{Label: "Open", Value: strconv.Itoa(open)},
		{Label: "New today", Value: strconv.Itoa(fresh)},
	}}
	var parts []string
	if fresh > 0 {
		parts = append(parts, countWord(fresh, "new issue", "new issues")+" today")
	}
	switch {
	case back > 0 && fresh > 0:
		parts = append(parts, strconv.Itoa(back)+" came back")
	case back > 0:
		parts = append(parts, strconv.Itoa(back)+" came back today")
	}
	out.Line = strings.Join(parts, ", ")
	for _, b := range busy {
		out.Items = append(out.Items, core.WidgetItem{ID: itoa(b.ID), Title: b.Title, Detail: joinNonEmpty(" · ", b.ProjectName, timesWord(b.Events)+" today"), URL: issueURL(b.ID)})
	}
	return out, nil
}

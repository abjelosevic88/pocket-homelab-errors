package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/abjelosevic88/pocket-homelab-errors/internal/store"
)

// ---- issues ----

func (s *Server) handleSummary(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	uid := actorID(ctx)
	o, err := s.store.GetOverview(ctx, uid)
	if err != nil {
		s.fail(w, err, "summary")
		return
	}
	top, _, err := s.store.ListIssues(ctx, uid, store.IssueFilter{Status: "open", Limit: 5})
	if err != nil {
		s.fail(w, err, "summary")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"overview": o, "latest": top, "watch": s.watchStatus(uid)})
}

func (s *Server) handleListIssues(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := r.URL.Query()
	f := store.IssueFilter{Status: q.Get("status"), Q: q.Get("q"), Sort: q.Get("sort"), Limit: queryInt(r, "limit"), Offset: queryInt(r, "offset"), Level: q.Get("level")}
	if !oneOf(f.Status, []string{"", "open", "resolved", "ignored", "all"}) {
		writeErr(w, http.StatusBadRequest, "status is open, resolved, ignored or all")
		return
	}
	if f.Level != "" && !oneOf(f.Level, store.Levels) {
		writeErr(w, http.StatusBadRequest, "unknown level")
		return
	}
	f.ProjectID, _ = toInt64(q.Get("project"))
	if v := q.Get("since"); v != "" {
		d, err := time.ParseDuration(v)
		if n, ok := toInt64(strings.TrimSuffix(v, "d")); ok && strings.HasSuffix(v, "d") {
			d, err = time.Duration(n)*24*time.Hour, nil
		}
		if err != nil {
			writeErr(w, http.StatusBadRequest, "since is a length like 24h or 7d")
			return
		}
		f.Since = store.Stamp(time.Now().Add(-d))
	}
	list, total, err := s.store.ListIssues(ctx, actorID(ctx), f)
	if err != nil {
		s.fail(w, err, "issues")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"issues": list, "total": total})
}

// issueView is an issue with what its page needs.
type issueView struct {
	*store.Issue
	URL     string                    `json:"url"`
	Project *store.Project            `json:"project"`
	Links   []*store.IssueLink        `json:"links"`
	Tags    map[string]map[string]int `json:"tags"`
}

func (s *Server) handleGetIssue(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	uid := actorID(ctx)
	is, err := s.store.GetIssue(ctx, uid, idParam(r, "id"))
	if err != nil {
		s.fail(w, err, "issue")
		return
	}
	p, err := s.store.GetProject(ctx, uid, is.ProjectID)
	if err != nil {
		s.fail(w, err, "issue")
		return
	}
	links, _ := s.store.ListLinks(ctx, uid, is.ID)
	tags, _ := s.store.TagCounts(ctx, uid, is.ID)
	writeJSON(w, http.StatusOK, issueView{Issue: is, URL: s.link("/issues/" + itoa(is.ID)), Project: p, Links: links, Tags: tags})
}

// handleIssueHours is the chart: range=24h (by hour) or 30d (by day).
func (s *Server) handleIssueHours(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	uid := actorID(ctx)
	is, err := s.store.GetIssue(ctx, uid, idParam(r, "id"))
	if err != nil {
		s.fail(w, err, "issue")
		return
	}
	var from time.Time
	var n int
	byDay := r.URL.Query().Get("range") == "30d"
	if byDay {
		t := time.Now().AddDate(0, 0, -29)
		from, n = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.Local), 30
	} else {
		from, n = time.Now().UTC().Add(-23*time.Hour).Truncate(time.Hour), 24
	}
	b, err := s.store.IssueHours(ctx, uid, is.ID, from, n, byDay)
	if err != nil {
		s.fail(w, err, "chart")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"from": from.Format(time.RFC3339), "by": map[bool]string{true: "day", false: "hour"}[byDay], "counts": b})
}

// eventRow is an event in a list: what it is without its stack.
type eventRow struct {
	ID          int64  `json:"id"`
	At          string `json:"at"`
	Level       string `json:"level"`
	Message     string `json:"message"`
	Environment string `json:"environment"`
	Release     string `json:"release"`
	Who         string `json:"who"`
	URL         string `json:"url"`
}

func (s *Server) handleListEvents(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	uid := actorID(ctx)
	before, _ := toInt64(r.URL.Query().Get("before"))
	if _, err := s.store.GetIssue(ctx, uid, idParam(r, "id")); err != nil {
		s.fail(w, err, "issue")
		return
	}
	list, err := s.store.ListEvents(ctx, uid, idParam(r, "id"), before, queryInt(r, "limit"))
	if err != nil {
		s.fail(w, err, "events")
		return
	}
	out := make([]eventRow, 0, len(list))
	for _, e := range list {
		out = append(out, eventRow{ID: e.ID, At: e.At, Level: e.Level, Message: e.Message, Environment: e.Environment, Release: e.Release, Who: e.Who, URL: e.Data.RequestURL()})
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": out})
}

func (s *Server) handleGetEvent(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	uid := actorID(ctx)
	iid := idParam(r, "id")
	eid := idParam(r, "eid")
	if _, err := s.store.GetIssue(ctx, uid, iid); err != nil {
		s.fail(w, err, "issue")
		return
	}
	e, err := s.store.GetEvent(ctx, uid, iid, eid)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) && eid == 0 {
			writeJSON(w, http.StatusOK, map[string]any{"event": nil, "pos": 0, "total": 0})
			return
		}
		s.fail(w, err, "event")
		return
	}
	pos, total, newer, older := s.store.EventPlace(ctx, uid, iid, e.ID)
	writeJSON(w, http.StatusOK, map[string]any{"event": e, "pos": pos, "total": total, "newer": newer, "older": older})
}

// statusIn changes where issues stand.
type statusIn struct {
	IDs    []int64 `json:"ids"`
	Status string  `json:"status"` // resolved | ignored | open
	For    string  `json:"for"`    // ignored for: 1h, 24h, 7d, 30d; "" = for good
}

func (in statusIn) check() (string, error) {
	if !oneOf(in.Status, []string{"resolved", "ignored", "open"}) {
		return "", &userErr{"status is resolved, ignored or open"}
	}
	if in.Status != "ignored" || in.For == "" {
		return "", nil
	}
	d, ok := map[string]time.Duration{"1h": time.Hour, "24h": 24 * time.Hour, "7d": 7 * 24 * time.Hour, "30d": 30 * 24 * time.Hour}[in.For]
	if !ok {
		return "", &userErr{"ignore for 1h, 24h, 7d, 30d or for good"}
	}
	return store.Stamp(time.Now().Add(d)), nil
}

func (s *Server) handleIssueStatus(w http.ResponseWriter, r *http.Request) {
	var in statusIn
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	in.IDs = []int64{idParam(r, "id")}
	s.setStatus(w, r, in, true)
}

func (s *Server) handleIssuesStatus(w http.ResponseWriter, r *http.Request) {
	var in statusIn
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if len(in.IDs) == 0 || len(in.IDs) > 500 {
		writeErr(w, http.StatusBadRequest, "say which issues (ids, up to 500)")
		return
	}
	s.setStatus(w, r, in, false)
}

func (s *Server) setStatus(w http.ResponseWriter, r *http.Request, in statusIn, one bool) {
	ctx := r.Context()
	uid := actorID(ctx)
	until, err := in.check()
	if err != nil {
		s.userFail(w, err, "status")
		return
	}
	// an app acting through a link changes only its own project's issues
	if app := linkApp(ctx); app != "" {
		ids := in.IDs[:0]
		for _, id := range in.IDs {
			if is, err := s.store.GetIssue(ctx, uid, id); err == nil {
				if p, err := s.store.GetProject(ctx, uid, is.ProjectID); err == nil && p.Source == app {
					ids = append(ids, id)
				}
			}
		}
		if len(ids) == 0 {
			writeErr(w, http.StatusForbidden, "an app may change only the issues of its own project")
			return
		}
		in.IDs = ids
	}
	changed, err := s.store.SetStatus(ctx, uid, in.IDs, in.Status, until)
	if err != nil {
		s.fail(w, err, "status")
		return
	}
	if in.Status == "resolved" {
		for _, id := range changed {
			if is, err := s.store.GetIssue(ctx, uid, id); err == nil {
				s.publish(ctx, uid, "errors.issue_resolved", itoa(id), map[string]any{"issue_id": id, "project_id": is.ProjectID, "project": is.ProjectName,
					"title": is.Title, "url": s.link("/issues/" + itoa(id))})
			}
		}
	}
	if one {
		if len(changed) == 0 {
			writeErr(w, http.StatusNotFound, "not found")
			return
		}
		is, err := s.store.GetIssue(ctx, uid, changed[0])
		if err != nil {
			s.fail(w, err, "issue")
			return
		}
		writeJSON(w, http.StatusOK, is)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"changed": changed})
}

func (s *Server) handleUpdateIssue(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	uid := actorID(ctx)
	var in struct {
		Note *string `json:"note"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if in.Note != nil {
		if err := s.store.SetNote(ctx, uid, idParam(r, "id"), clip(strings.TrimSpace(*in.Note), 20000)); err != nil {
			s.fail(w, err, "note")
			return
		}
	}
	is, err := s.store.GetIssue(ctx, uid, idParam(r, "id"))
	if err != nil {
		s.fail(w, err, "issue")
		return
	}
	writeJSON(w, http.StatusOK, is)
}

func (s *Server) handleDeleteIssue(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if err := s.store.DeleteIssue(ctx, actorID(ctx), idParam(r, "id")); err != nil {
		s.fail(w, err, "delete")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- an issue as text (copy, Pocket Projects, Pocket Tasks, Pocket AI) ----

func (s *Server) handleIssueMarkdown(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	md, _, _, err := s.issueMarkdown(ctx, actorID(ctx), idParam(r, "id"), true)
	if err != nil {
		s.fail(w, err, "issue")
		return
	}
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	_, _ = w.Write([]byte(md))
}

// issueMarkdown writes an issue and its newest event as Markdown: what,
// where, how often, the stack (the app's own frames), the request, the
// last breadcrumbs and log lines.
func (s *Server) issueMarkdown(ctx context.Context, uid, id int64, withLink bool) (string, *store.Issue, *store.Project, error) {
	is, err := s.store.GetIssue(ctx, uid, id)
	if err != nil {
		return "", nil, nil, err
	}
	p, err := s.store.GetProject(ctx, uid, is.ProjectID)
	if err != nil {
		return "", nil, nil, err
	}
	ev, _ := s.store.GetEvent(ctx, uid, is.ID, 0)
	var b strings.Builder
	fmt.Fprintf(&b, "**%s** in %s\n\n", is.Title, p.Name)
	if is.Culprit != "" {
		fmt.Fprintf(&b, "- Where: `%s`\n", is.Culprit)
	}
	fmt.Fprintf(&b, "- Level: %s · %d times · %d people\n", is.Level, is.Times, is.People)
	fmt.Fprintf(&b, "- First seen %s, last seen %s\n", humanStamp(is.FirstSeen), humanStamp(is.LastSeen))
	if is.FirstRelease != "" || is.LastRelease != "" {
		fmt.Fprintf(&b, "- Releases: %s → %s\n", firstNonEmpty(is.FirstRelease, "?"), firstNonEmpty(is.LastRelease, "?"))
	}
	if is.Environment != "" {
		fmt.Fprintf(&b, "- Environment: %s\n", is.Environment)
	}
	if ev != nil {
		d := ev.Data
		for i := len(d.Exceptions) - 1; i >= 0; i-- {
			e := d.Exceptions[i]
			head := strings.TrimSpace(e.Type + ": " + e.Value)
			if i < len(d.Exceptions)-1 {
				head = "Caused by " + head
			}
			fmt.Fprintf(&b, "\n%s\n", strings.Trim(head, ": "))
			if len(e.Frames) > 0 {
				b.WriteString("```\n")
				shown := 0
				for k := len(e.Frames) - 1; k >= 0 && shown < 25; k-- {
					f := e.Frames[k]
					if !f.InApp && shown > 0 && k < len(e.Frames)-3 {
						continue
					}
					b.WriteString(frameLine(f) + "\n")
					if f.ContextLine != "" && f.InApp {
						b.WriteString("    > " + strings.TrimSpace(f.ContextLine) + "\n")
					}
					shown++
				}
				b.WriteString("```\n")
			}
		}
		if len(d.Exceptions) == 0 && d.Message != "" {
			fmt.Fprintf(&b, "\n```\n%s\n```\n", clip(d.Message, 2000))
		}
		if d.Request != nil && d.Request.URL != "" {
			fmt.Fprintf(&b, "\nRequest: %s %s\n", d.Request.Method, d.Request.URL)
		}
		if n := len(d.Breadcrumbs); n > 0 {
			b.WriteString("\nBefore it:\n")
			for _, c := range d.Breadcrumbs[max(0, n-10):] {
				fmt.Fprintf(&b, "- %s %s\n", firstNonEmpty(c.Category, c.Type), clip(firstNonEmpty(c.Message, mapText(c.Data)), 200))
			}
		}
		if len(d.LogLines) > 0 {
			b.WriteString("\nLog:\n```\n")
			for _, l := range d.LogLines[max(0, len(d.LogLines)-20):] {
				b.WriteString(clip(l, 400) + "\n")
			}
			b.WriteString("```\n")
		}
	}
	if is.Note != "" {
		fmt.Fprintf(&b, "\nNote: %s\n", is.Note)
	}
	if withLink {
		fmt.Fprintf(&b, "\n[Open in Pocket Errors](%s)\n", s.link("/issues/"+itoa(is.ID)))
	}
	return b.String(), is, p, nil
}

func frameLine(f store.Frame) string {
	fn := f.Function
	if f.Module != "" && !strings.Contains(fn, f.Module) {
		fn = f.Module + "." + fn
	}
	loc := f.File
	if f.Line > 0 {
		loc += fmt.Sprintf(":%d", f.Line)
	}
	mark := "  "
	if f.InApp {
		mark = "→ "
	}
	return strings.TrimRight(mark+fn+"  "+loc, " ")
}

func mapText(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+m[k])
	}
	return strings.Join(parts, " ")
}

func humanStamp(v string) string {
	t, err := time.Parse(time.RFC3339Nano, v)
	if err != nil {
		return v
	}
	return t.In(time.Local).Format("2 Jan 2006 15:04")
}

// ---- to the other apps ----

// handleSendIssue is POST /issues/{id}/send {to: projects|tasks, title, team}:
// the issue becomes a Pocket Projects issue or a Pocket Tasks task with what
// broke and a link back; the link is kept on the issue.
func (s *Server) handleSendIssue(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	uid := actorID(ctx)
	var in struct {
		To    string `json:"to"`
		Title string `json:"title"`
		Team  string `json:"team"`
		Due   string `json:"due"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	md, is, p, err := s.issueMarkdown(ctx, uid, idParam(r, "id"), true)
	if err != nil {
		s.fail(w, err, "issue")
		return
	}
	c := s.clients(ctx, uid)
	title := clip(firstNonEmpty(strings.TrimSpace(in.Title), p.Name+": "+is.Title), 200)
	link := &store.IssueLink{IssueID: is.ID}
	switch in.To {
	case "projects":
		sib, err := c.conn(ctx, appProjects, scopeProjectsWrite)
		if err != nil {
			s.userFail(w, err, "Pocket Projects")
			return
		}
		team := firstNonEmpty(strings.TrimSpace(in.Team), c.st.ProjectsTeam)
		if team == "" {
			writeErr(w, http.StatusBadRequest, "pick the team the issue goes to")
			return
		}
		body := map[string]any{"team": team, "title": title, "description": md, "source_ref": "errors:" + itoa(is.ID), "source_url": s.link("/issues/" + itoa(is.ID)),
			"priority": map[string]int{"fatal": 1, "error": 2, "warning": 3}[is.Level]}
		res, err := postJSONTo(ctx, sib, "/api/v1/issues", body)
		if err != nil {
			s.userFail(w, &userErr{"Pocket Projects: " + err.Error()}, "Pocket Projects")
			return
		}
		ref := firstNonEmpty(str(res, "identifier"), idOf(res))
		link.App, link.Ref, link.Title, link.URL = "projects", ref, firstNonEmpty(str(res, "title"), title), linkTo(sib, "/issue/"+url.PathEscape(ref))
		if c.st.ProjectsTeam == "" {
			c.st.ProjectsTeam = team
			_ = s.store.SaveUserSettings(ctx, uid, c.st)
		}
	case "tasks":
		sib, err := c.conn(ctx, appTasks, scopeTasksWrite)
		if err != nil {
			s.userFail(w, err, "Pocket Tasks")
			return
		}
		body := map[string]any{"title": "Look into: " + title, "notes": clip(md, 20000), "url": s.link("/issues/" + itoa(is.ID))}
		if in.Due != "" {
			if !validDate(in.Due) {
				writeErr(w, http.StatusBadRequest, "the due day is YYYY-MM-DD")
				return
			}
			body["due"] = in.Due
		}
		res, err := postJSONTo(ctx, sib, "/api/v1/tasks", body)
		if err != nil {
			s.userFail(w, &userErr{"Pocket Tasks: " + err.Error()}, "Pocket Tasks")
			return
		}
		link.App, link.Ref, link.Title, link.URL = "tasks", idOf(res), firstNonEmpty(str(res, "title"), title), linkTo(sib, "/today?task="+idOf(res))
	default:
		writeErr(w, http.StatusBadRequest, "send to projects or tasks")
		return
	}
	if err := s.store.AddLink(ctx, uid, link); err != nil {
		s.fail(w, err, "link")
		return
	}
	writeJSON(w, http.StatusCreated, link)
}

func (s *Server) handleDeleteLink(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if err := s.store.DeleteLink(ctx, actorID(ctx), idParam(r, "id"), idParam(r, "lid")); err != nil {
		s.fail(w, err, "link")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleTeams lists the Pocket Projects teams the person can file an issue in.
func (s *Server) handleTeams(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	c := s.clients(ctx, actorID(ctx))
	sib, err := c.conn(ctx, appProjects, scopeProjectsRead)
	if err != nil {
		s.userFail(w, err, "Pocket Projects")
		return
	}
	v, err := getJSONFrom(ctx, sib, "/api/v1/teams", nil)
	if err != nil {
		s.userFail(w, &userErr{"Pocket Projects: " + err.Error()}, "teams")
		return
	}
	out := []map[string]string{}
	for _, t := range rows(v, "teams") {
		if key := str(t, "key"); key != "" {
			out = append(out, map[string]string{"key": key, "name": firstNonEmpty(str(t, "name"), key)})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"teams": out, "default": c.st.ProjectsTeam})
}

// ---- search and the day ----

type found struct {
	Type     string `json:"type"` // issue | project
	ID       int64  `json:"id"`
	Title    string `json:"title"`
	Subtitle string `json:"subtitle"`
	URL      string `json:"url"`
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	uid := actorID(ctx)
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	out := []found{}
	if q == "" {
		writeJSON(w, http.StatusOK, map[string]any{"results": out})
		return
	}
	projects, err := s.store.ListProjects(ctx, uid, true)
	if err != nil {
		s.fail(w, err, "search")
		return
	}
	lq := strings.ToLower(q)
	for _, p := range projects {
		if strings.Contains(strings.ToLower(p.Name+" "+p.Container), lq) {
			out = append(out, found{Type: "project", ID: p.ID, Title: p.Name, Subtitle: fmt.Sprintf("%d open", p.Open), URL: s.link("/projects/" + itoa(p.ID))})
		}
	}
	list, _, err := s.store.ListIssues(ctx, uid, store.IssueFilter{Status: "all", Q: q, Limit: 30})
	if err != nil {
		s.fail(w, err, "search")
		return
	}
	for _, is := range list {
		out = append(out, found{Type: "issue", ID: is.ID, Title: is.Title, Subtitle: joinNonEmpty(" · ", is.ProjectName, is.Culprit, statusWord(is.Status)),
			URL: s.link("/issues/" + itoa(is.ID))})
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": out})
}

func statusWord(st string) string {
	return map[string]string{"open": "open", "resolved": "resolved", "ignored": "ignored"}[st]
}

// handleDay is one local day: the issues first seen that day and how many
// errors each project had.
func (s *Server) handleDay(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	uid := actorID(ctx)
	date := chiParam(r, "date")
	day, err := time.ParseInLocation("2006-01-02", date, time.Local)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "the day is YYYY-MM-DD")
		return
	}
	from, to := store.Stamp(day), store.Stamp(day.AddDate(0, 0, 1))
	all, _, err := s.store.ListIssues(ctx, uid, store.IssueFilter{Status: "all", Since: from, Limit: 500})
	if err != nil {
		s.fail(w, err, "day")
		return
	}
	newIssues := []map[string]any{}
	for _, is := range all {
		if is.FirstSeen >= from && is.FirstSeen < to {
			newIssues = append(newIssues, map[string]any{"id": is.ID, "title": is.Title, "project": is.ProjectName, "level": is.Level, "status": is.Status,
				"first_seen": is.FirstSeen, "url": s.link("/issues/" + itoa(is.ID))})
		}
	}
	counts, total, err := s.dayCounts(ctx, uid, day)
	if err != nil {
		s.fail(w, err, "day")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"date": date, "new": newIssues, "events": total, "projects": counts, "url": s.link("/?day=" + date)})
}

// dayCounts is how many errors each project had on a local day.
func (s *Server) dayCounts(ctx context.Context, uid int64, day time.Time) ([]map[string]any, int, error) {
	projects, err := s.store.ListProjects(ctx, uid, true)
	if err != nil {
		return nil, 0, err
	}
	out := []map[string]any{}
	total := 0
	for _, p := range projects {
		n, err := s.store.ProjectEventsBetween(ctx, uid, p.ID, day, day.AddDate(0, 0, 1))
		if err != nil {
			return nil, 0, err
		}
		if n > 0 {
			out = append(out, map[string]any{"project_id": p.ID, "project": p.Name, "events": n})
			total += n
		}
	}
	return out, total, nil
}

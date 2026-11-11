package server

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/abjelosevic88/pocket-homelab-errors/internal/logs"
)

// The lines can say anything an app wrote (addresses, names, now and then
// a secret Scrub missed), so only admins read them, also through a link.
func (s *Server) logsAdmin(w http.ResponseWriter, r *http.Request) bool {
	if s.logs == nil {
		writeErr(w, http.StatusServiceUnavailable, "the logs are not open yet")
		return false
	}
	if u := userFrom(r.Context()); u == nil || u.Role != "admin" {
		writeErr(w, http.StatusForbidden, "only an admin can read this box's logs")
		return false
	}
	return true
}

var reSpan = regexp.MustCompile(`^(\d{1,4})(m|h|d)$`)

func parseSpan(v string) (time.Duration, bool) {
	m := reSpan.FindStringSubmatch(strings.TrimSpace(v))
	if m == nil {
		return 0, false
	}
	n, _ := strconv.Atoi(m[1])
	if n <= 0 {
		return 0, false
	}
	unit := map[string]time.Duration{"m": time.Minute, "h": time.Hour, "d": 24 * time.Hour}[m[2]]
	return time.Duration(n) * unit, true
}

// parseWhen reads unix ms, unix seconds or RFC 3339.
func parseWhen(v string) int64 {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if n, err := strconv.ParseInt(v, 10, 64); err == nil {
		if n < 1e11 {
			return n * 1000
		}
		return n
	}
	for _, f := range []string{time.RFC3339Nano, "2006-01-02T15:04", "2006-01-02 15:04", "2006-01-02"} {
		if t, err := time.ParseInLocation(f, v, time.Local); err == nil {
			return t.UnixMilli()
		}
	}
	return 0
}

// span reads range= (or since=: 30m, 24h, 7d) or from=/to=, default def.
func span(r *http.Request, def time.Duration) (int64, int64) {
	q := r.URL.Query()
	now := time.Now().UnixMilli()
	from, to := parseWhen(q.Get("from")), parseWhen(q.Get("to"))
	if from > 0 {
		if to <= from {
			to = now
		}
		return from, to
	}
	d := def
	for _, k := range []string{"range", "since"} {
		if x, ok := parseSpan(q.Get(k)); ok {
			d = x
			break
		}
	}
	if d > 92*24*time.Hour {
		d = 92 * 24 * time.Hour
	}
	if to <= 0 {
		to = now
	}
	return to - d.Milliseconds(), to
}

// filter reads the line filter: q, source (ids or names, comma separated),
// group, level (that level and worse), pattern, before/after, limit.
func (s *Server) filter(r *http.Request, def time.Duration) logs.Filter {
	q := r.URL.Query()
	f := logs.Filter{Q: strings.TrimSpace(q.Get("q")), Limit: queryInt(r, "limit")}
	if q.Get("from") != "" || q.Get("range") != "" || q.Get("since") != "" || def > 0 {
		f.From, f.To = span(r, def)
	}
	for _, v := range strings.Split(q.Get("source"), ",") {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if id, err := strconv.ParseInt(v, 10, 64); err == nil {
			f.Sources = append(f.Sources, id)
		} else if src := s.logs.SourceByName(v); src != nil {
			f.Sources = append(f.Sources, src.ID)
		} else {
			for _, x := range s.logs.AllSources() {
				if strings.EqualFold(x.Label, v) {
					f.Sources = append(f.Sources, x.ID)
				}
			}
			if len(f.Sources) == 0 {
				f.Sources = []int64{-1}
			}
		}
	}
	for _, g := range strings.Split(q.Get("group"), ",") {
		if g = strings.TrimSpace(g); g != "" {
			f.Groups = append(f.Groups, g)
		}
	}
	f.MinLevel = logs.ParseLevel(q.Get("level"))
	f.Pattern, _ = strconv.ParseInt(q.Get("pattern"), 10, 64)
	f.Before, _ = strconv.ParseInt(q.Get("before"), 10, 64)
	f.After, _ = strconv.ParseInt(q.Get("after"), 10, 64)
	return f
}

// GET /logs/status: how the reading goes, and how much is kept.
func (s *Server) handleLogsStatus(w http.ResponseWriter, r *http.Request) {
	if !s.logsAdmin(w, r) {
		return
	}
	ctx := r.Context()
	st := s.logSettings(ctx)
	used, file := s.logs.Size(ctx)
	lines, oldest, newest := s.logs.Counts(ctx)
	lc := s.logColl
	lc.mu.Lock()
	docker := map[string]any{"available": s.docker.available(), "error": lc.err, "containers": lc.read, "lines": lc.lines}
	if !lc.checked.IsZero() {
		docker["checked_at"] = lc.checked.UTC().Format(time.RFC3339)
	}
	lc.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"settings": st, "docker": docker, "journal": s.journal.Status(),
		"storage": map[string]any{"used": used, "file": file, "max": int64(st.MaxMB) << 20, "lines": lines, "oldest": oldest, "newest": newest},
		"sources": len(s.logs.AllSources())})
}

// PUT /logs/settings.
func (s *Server) handlePutLogsSettings(w http.ResponseWriter, r *http.Request) {
	if !s.logsAdmin(w, r) {
		return
	}
	ctx := r.Context()
	cur := s.logSettings(ctx)
	var in logSettings
	keys, err := decodeKeys(r, &in)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if keys["on"] {
		cur.On = in.On
	}
	if keys["journal"] {
		cur.Journal = in.Journal
	}
	if keys["pattern"] {
		p := strings.TrimSpace(in.Pattern)
		if p == "" {
			p = "*"
		}
		cur.Pattern = clip(p, 500)
	}
	if keys["keep_days"] {
		if in.KeepDays < 1 || in.KeepDays > 90 {
			writeErr(w, http.StatusBadRequest, "keep lines 1 to 90 days")
			return
		}
		cur.KeepDays = in.KeepDays
	}
	if keys["max_mb"] {
		if in.MaxMB < 100 || in.MaxMB > 200000 {
			writeErr(w, http.StatusBadRequest, "the logs may take 100 MB to 200 GB")
			return
		}
		cur.MaxMB = in.MaxMB
	}
	if err := s.saveLogSettings(ctx, cur); err != nil {
		s.fail(w, err, "save logs settings")
		return
	}
	if keys["keep_days"] || keys["max_mb"] {
		go s.tidyLogs(context.Background())
	}
	writeJSON(w, http.StatusOK, cur)
}

// POST /logs/clear drops every kept line and count.
func (s *Server) handleLogsClear(w http.ResponseWriter, r *http.Request) {
	if !s.logsAdmin(w, r) {
		return
	}
	if err := s.logs.Clear(r.Context()); err != nil {
		s.fail(w, err, "clear logs")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// GET /logs: lines, newest first.
func (s *Server) handleLogLines(w http.ResponseWriter, r *http.Request) {
	if !s.logsAdmin(w, r) {
		return
	}
	f := s.filter(r, 0)
	ls, more, err := s.logs.Lines(r.Context(), f)
	if err != nil {
		s.fail(w, err, "log lines")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"lines": ls, "more": more})
}

// GET /logs/histogram: the matching lines per bar and level.
func (s *Server) handleLogHistogram(w http.ResponseWriter, r *http.Request) {
	if !s.logsAdmin(w, r) {
		return
	}
	f := s.filter(r, 24*time.Hour)
	h, err := s.logs.LineHistogram(r.Context(), f)
	if err != nil {
		s.fail(w, err, "log histogram")
		return
	}
	writeJSON(w, http.StatusOK, h)
}

// GET /logs/lines/{id}/context: the lines of its source around it.
func (s *Server) handleLogContext(w http.ResponseWriter, r *http.Request) {
	if !s.logsAdmin(w, r) {
		return
	}
	ls, err := s.logs.Context(r.Context(), idParam(r, "id"), queryInt(r, "n"))
	if errors.Is(err, logs.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "that line is not kept any more")
		return
	}
	if err != nil {
		s.fail(w, err, "log context")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"lines": ls, "id": idParam(r, "id")})
}

// GET /logs/overview: every line written in a span, by level, source and bar.
func (s *Server) handleLogOverview(w http.ResponseWriter, r *http.Request) {
	if !s.logsAdmin(w, r) {
		return
	}
	from, to := span(r, 24*time.Hour)
	o, err := s.logs.Overview(r.Context(), from, to)
	if err != nil {
		s.fail(w, err, "log overview")
		return
	}
	writeJSON(w, http.StatusOK, o)
}

// GET /logs/patterns: the messages that repeat.
func (s *Server) handleLogPatterns(w http.ResponseWriter, r *http.Request) {
	if !s.logsAdmin(w, r) {
		return
	}
	from, to := span(r, 24*time.Hour)
	var src int64
	if f := s.filter(r, 0); len(f.Sources) == 1 {
		src = f.Sources[0]
	}
	ps, all, err := s.logs.Patterns(r.Context(), from, to, src, r.URL.Query().Get("sort"), queryInt(r, "limit"))
	if err != nil {
		s.fail(w, err, "log patterns")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"patterns": ps, "lines": all, "from": from, "to": to})
}

// GET /logs/web: the requests the proxy logged.
func (s *Server) handleLogWeb(w http.ResponseWriter, r *http.Request) {
	if !s.logsAdmin(w, r) {
		return
	}
	from, to := span(r, 24*time.Hour)
	wt, err := s.logs.WebTraffic(r.Context(), from, to)
	if err != nil {
		s.fail(w, err, "web traffic")
		return
	}
	writeJSON(w, http.StatusOK, wt)
}

// GET /logs/sources: every source with its lines in a span (zero too).
func (s *Server) handleLogSources(w http.ResponseWriter, r *http.Request) {
	if !s.logsAdmin(w, r) {
		return
	}
	from, to := span(r, 24*time.Hour)
	o, err := s.logs.Overview(r.Context(), from, to)
	if err != nil {
		s.fail(w, err, "log sources")
		return
	}
	seen := map[int64]bool{}
	out := o.Sources
	for _, sc := range out {
		seen[sc.ID] = true
	}
	for _, src := range s.logs.AllSources() {
		if !seen[src.ID] {
			out = append(out, logs.SourceCount{Source: src, Spark: make([]int, len(o.Volume.Levels["info"]))})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"sources": out, "bucket": o.Volume.Bucket, "from": o.Volume.From, "total": o.Totals["all"]})
}

// PUT /logs/sources/{id}: what is kept of a source's lines.
func (s *Server) handlePutLogSource(w http.ResponseWriter, r *http.Request) {
	if !s.logsAdmin(w, r) {
		return
	}
	var in struct {
		Keep string `json:"keep"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	src, err := s.logs.SetKeep(r.Context(), idParam(r, "id"), in.Keep)
	if errors.Is(err, logs.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, src)
}

// GET /logs/export.txt: the matching lines as text, oldest first (up to 50,000).
func (s *Server) handleLogExport(w http.ResponseWriter, r *http.Request) {
	if !s.logsAdmin(w, r) {
		return
	}
	f := s.filter(r, 0)
	var all []logs.Line
	f.Limit = 1000
	for len(all) < 50000 {
		ls, more, err := s.logs.Lines(r.Context(), f)
		if err != nil {
			s.fail(w, err, "export logs")
			return
		}
		all = append(all, ls...)
		if !more || len(ls) == 0 {
			break
		}
		f.Before = ls[len(ls)-1].ID
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="pocket-logs-%s.txt"`, time.Now().Format("20060102-1504")))
	bw := bufio.NewWriter(w)
	for i := len(all) - 1; i >= 0; i-- {
		l := all[i]
		fmt.Fprintf(bw, "%s %-7s %-24s %s\n", time.UnixMilli(l.At).Format("2006-01-02 15:04:05.000"), strings.ToUpper(l.Level), l.Label, l.Msg)
	}
	_ = bw.Flush()
}

// GET /issues/{id}/logs: the lines its container (or the device that sent
// it, when the project's source is a device's) wrote around an event
// (event = an id, default the latest), for the issue page.
func (s *Server) handleIssueLogs(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	uid := actorID(ctx)
	is, err := s.store.GetIssue(ctx, uid, idParam(r, "id"))
	if err != nil {
		s.fail(w, err, "issue")
		return
	}
	u := userFrom(ctx)
	out := map[string]any{"lines": []logs.Line{}, "source": nil, "admin": u != nil && u.Role == "admin", "on": false}
	if s.logs == nil || u == nil || u.Role != "admin" {
		writeJSON(w, http.StatusOK, out)
		return
	}
	out["on"] = s.logSettings(ctx).On
	ev, err := s.store.GetEvent(ctx, uid, is.ID, int64(queryInt(r, "event")))
	if err != nil {
		writeJSON(w, http.StatusOK, out)
		return
	}
	p, err := s.store.GetProject(ctx, uid, is.ProjectID)
	if err != nil {
		writeJSON(w, http.StatusOK, out)
		return
	}
	var src *logs.Source
	if p.Source != "" && p.Source != "docker" {
		src = s.logs.SourceByKey("device", p.Source) // a phone that sends its lines too
	}
	for _, name := range []string{p.Container, ev.Data.ServerName, "pocket-" + p.AppSlug} {
		if src == nil && name != "" && name != "pocket-" {
			src = s.logs.SourceByName(name)
		}
	}
	if src == nil {
		writeJSON(w, http.StatusOK, out)
		return
	}
	at, err := time.Parse(time.RFC3339Nano, ev.At)
	if err != nil {
		writeJSON(w, http.StatusOK, out)
		return
	}
	ls, err := s.logs.Around(ctx, src.ID, at, 4*time.Minute, 120)
	if err != nil {
		s.fail(w, err, "issue logs")
		return
	}
	// the line nearest the event
	var hit int64
	best := int64(1 << 62)
	for _, l := range ls {
		d := l.At - at.UnixMilli()
		if d < 0 {
			d = -d
		}
		if d < best || d == best && (l.Level == "error" || l.Level == "fatal") {
			best, hit = d, l.ID
		}
	}
	out["lines"], out["source"], out["hit"], out["at"] = ls, src, hit, at.UnixMilli()
	writeJSON(w, http.StatusOK, out)
}

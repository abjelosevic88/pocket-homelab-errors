package server

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/abjelosevic88/pocket-homelab-errors/internal/intake"
	"github.com/abjelosevic88/pocket-homelab-errors/internal/store"
)

// ---- errors coming in ----
//
// Three doors, one way through (ingest):
//   - Sentry's: POST /api/{project}/store/ and /api/{project}/envelope/ with
//     the project's key (X-Sentry-Auth, ?sentry_key= or the envelope's dsn),
//     so any Sentry SDK — browser, Go, Python, Node — sends here unchanged;
//   - the plain JSON API: POST /api/v1/projects/{id}/events with a token;
//   - Pocket apps through a Core link (errors:write): POST /api/v1/report,
//     into a project named after the app.
// The containers' logs (watch.go) come in through the same ingest.

// defaultRate is how many events a project may send a minute; more are
// turned away (429) so a loop in one app cannot fill the disk.
const defaultRate = 600

type minuteCount struct {
	at time.Time
	n  int
}

var errTooMany = errors.New("too many events this minute")

// allow counts one event against the project's minute.
func (s *Server) allow(p *store.Project) bool {
	limit := p.RateLimit
	if limit <= 0 {
		limit = defaultRate
	}
	now := time.Now().Truncate(time.Minute)
	s.rateMu.Lock()
	defer s.rateMu.Unlock()
	c := s.rate[p.ID]
	if c == nil || !c.at.Equal(now) {
		c = &minuteCount{at: now}
		s.rate[p.ID] = c
	}
	c.n++
	return c.n <= limit
}

// ingest files one event for a project: cleaned of secrets, grouped,
// counted; the person is told when it is new, back or busy.
func (s *Server) ingest(ctx context.Context, p *store.Project, ev *intake.Event) (store.RecordResult, error) {
	if p.Archived {
		return store.RecordResult{Outcome: store.OutcomeDuplicate}, nil
	}
	if !s.allow(p) {
		return store.RecordResult{}, errTooMany
	}
	st, _ := s.store.GetSettings(ctx, p.UserID)
	ip := ipOf(ev)
	scrub(&ev.Data, st.ScrubIPs)
	if st.ScrubIPs && ip != "" && ev.Who == ip {
		ev.Who = hashWho(ip)
	}
	if ev.EventID == "" {
		ev.EventID = intake.NewEventID()
	}
	if ev.At.IsZero() {
		ev.At = time.Now()
	}
	d := &ev.Data
	in := store.Incoming{EventID: ev.EventID, At: ev.At, Fingerprint: intake.Fingerprint(p.ID, d), Title: intake.Title(d), Culprit: intake.Culprit(d),
		Type: intake.Type(d), Level: ev.Level, Message: clip(firstNonEmpty(d.Message, mainValue(d)), 1000), Environment: ev.Environment, Release: ev.Release,
		Platform: firstNonEmpty(ev.Platform, p.Platform), Who: ev.Who, Data: *d}
	res, err := s.store.Record(context.WithoutCancel(ctx), p, in)
	if err != nil || res.Issue == nil {
		return res, err
	}
	s.afterRecord(p, res)
	return res, nil
}

func mainValue(d *store.EventData) string {
	if e := intake.Main(d); e != nil {
		return e.Value
	}
	return ""
}

func ipOf(ev *intake.Event) string {
	if ev.Data.User != nil {
		return ev.Data.User["ip_address"]
	}
	return ""
}

// hashWho stands for an address without keeping it: the same address
// counts as the same person.
func hashWho(v string) string {
	if v == "" {
		return ""
	}
	h := sha1.Sum([]byte("pocket-errors:" + v))
	return "anon:" + hex.EncodeToString(h[:6])
}

// secretWords mark values that are never kept (headers, query, data, extra).
var secretWords = []string{"authorization", "cookie", "password", "passwd", "secret", "token", "api_key", "apikey", "api-key", "session", "csrf", "credit", "card", "private"}

func isSecret(k string) bool {
	k = strings.ToLower(k)
	for _, w := range secretWords {
		if strings.Contains(k, w) {
			return true
		}
	}
	return false
}

const filtered = "[filtered]"

// scrub takes secrets (and, when asked, the app users' addresses) out of an
// event before anything is kept.
func scrub(d *store.EventData, ips bool) {
	if r := d.Request; r != nil {
		hs := r.Headers[:0]
		for _, h := range r.Headers {
			if isSecret(h[0]) {
				h[1] = filtered
			}
			if ips && oneOf(strings.ToLower(h[0]), []string{"x-forwarded-for", "x-real-ip", "cf-connecting-ip", "true-client-ip", "forwarded"}) {
				continue
			}
			hs = append(hs, h)
		}
		r.Headers = hs
		r.Query = scrubQuery(r.Query)
		if u, err := url.Parse(r.URL); err == nil && u.RawQuery != "" {
			u.RawQuery = scrubQuery(u.RawQuery)
			r.URL = u.String()
		}
		if r.Data != "" {
			var m map[string]any
			if json.Unmarshal([]byte(r.Data), &m) == nil {
				scrubMap(m, 0)
				b, _ := json.Marshal(m)
				r.Data = string(b)
			} else if strings.Contains(r.Data, "=") && !strings.ContainsAny(r.Data, "\n{") {
				r.Data = scrubQuery(r.Data)
			}
		}
	}
	if ips && d.User != nil {
		delete(d.User, "ip_address")
		if len(d.User) == 0 {
			d.User = nil
		}
	}
	scrubMap(d.Extra, 0)
	for i := range d.Breadcrumbs {
		for k := range d.Breadcrumbs[i].Data {
			if isSecret(k) {
				d.Breadcrumbs[i].Data[k] = filtered
			}
		}
	}
	for i := range d.Tags {
		if isSecret(d.Tags[i][0]) {
			d.Tags[i][1] = filtered
		}
	}
}

func scrubQuery(q string) string {
	if q == "" {
		return q
	}
	vals, err := url.ParseQuery(q)
	if err != nil {
		return q
	}
	changed := false
	for k := range vals {
		if isSecret(k) {
			vals[k] = []string{filtered}
			changed = true
		}
	}
	if !changed {
		return q
	}
	return vals.Encode()
}

func scrubMap(m map[string]any, depth int) {
	if depth > 5 {
		return
	}
	for k, v := range m {
		if isSecret(k) {
			m[k] = filtered
			continue
		}
		if sub, ok := v.(map[string]any); ok {
			scrubMap(sub, depth+1)
		}
	}
}

// ---- Sentry's door ----

// sentryCORS lets browser SDKs on any site send here.
func sentryCORS(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Access-Control-Allow-Origin", "*")
	h.Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	h.Set("Access-Control-Allow-Headers", "Content-Type, X-Sentry-Auth, Authorization, Content-Encoding, sentry-trace, baggage")
	h.Set("Access-Control-Max-Age", "86400")
}

func (s *Server) handleSentryPreflight(w http.ResponseWriter, r *http.Request) {
	sentryCORS(w)
	w.WriteHeader(http.StatusNoContent)
}

// sentryBody reads a request body, unpacking gzip and deflate.
func (s *Server) sentryBody(r *http.Request) ([]byte, error) {
	var rd io.Reader = http.MaxBytesReader(nil, r.Body, 20<<20)
	switch strings.ToLower(r.Header.Get("Content-Encoding")) {
	case "gzip":
		gz, err := gzip.NewReader(rd)
		if err != nil {
			return nil, err
		}
		defer gz.Close()
		rd = gz
	case "deflate":
		b, err := io.ReadAll(rd)
		if err != nil {
			return nil, err
		}
		if z, err := zlib.NewReader(bytes.NewReader(b)); err == nil {
			rd = z
		} else {
			rd = flate.NewReader(bytes.NewReader(b))
		}
	}
	return io.ReadAll(io.LimitReader(rd, 40<<20))
}

// sentryProject checks the key against the project in the path.
func (s *Server) sentryProject(r *http.Request, dsn string) (*store.Project, int, string) {
	key := firstNonEmpty(intake.SentryKey(r.Header.Get("X-Sentry-Auth")), r.URL.Query().Get("sentry_key"), intake.KeyFromDSN(dsn))
	if key == "" && strings.HasPrefix(strings.ToLower(r.Header.Get("Authorization")), "sentry ") {
		key = intake.SentryKey(r.Header.Get("Authorization"))
	}
	if key == "" {
		return nil, http.StatusUnauthorized, "missing the project key (sentry_key)"
	}
	p, err := s.store.ProjectByKey(r.Context(), key)
	if err != nil || itoa(p.ID) != chi.URLParam(r, "pid") {
		return nil, http.StatusUnauthorized, "unknown project key"
	}
	return p, 0, ""
}

func (s *Server) handleSentryStore(w http.ResponseWriter, r *http.Request) {
	sentryCORS(w)
	p, code, msg := s.sentryProject(r, "")
	if p == nil {
		writeJSON(w, code, map[string]string{"detail": msg})
		return
	}
	b, err := s.sentryBody(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"detail": "could not read the body"})
		return
	}
	ev, err := intake.ParseSentryEvent(b)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"detail": err.Error()})
		return
	}
	if _, err := s.ingest(r.Context(), p, ev); err != nil {
		s.ingestFail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": ev.EventID})
}

func (s *Server) handleSentryEnvelope(w http.ResponseWriter, r *http.Request) {
	sentryCORS(w)
	b, err := s.sentryBody(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"detail": "could not read the body"})
		return
	}
	evs, dsn, perr := intake.ParseEnvelope(b)
	p, code, msg := s.sentryProject(r, dsn)
	if p == nil {
		writeJSON(w, code, map[string]string{"detail": msg})
		return
	}
	if errors.Is(perr, intake.ErrNoEvent) {
		writeJSON(w, http.StatusOK, map[string]string{})
		return
	}
	if perr != nil && len(evs) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"detail": perr.Error()})
		return
	}
	id := ""
	for _, ev := range evs {
		if _, err := s.ingest(r.Context(), p, ev); err != nil {
			s.ingestFail(w, err)
			return
		}
		id = ev.EventID
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": id})
}

func (s *Server) ingestFail(w http.ResponseWriter, err error) {
	if errors.Is(err, errTooMany) {
		w.Header().Set("Retry-After", "60")
		w.Header().Set("X-Sentry-Rate-Limits", "60::project")
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"detail": err.Error()})
		return
	}
	s.log.Error("keeping an event failed", "err", err)
	writeJSON(w, http.StatusInternalServerError, map[string]string{"detail": "could not keep the event"})
}

// ---- the plain JSON API ----

// handleProjectEvent is POST /projects/{id}/events: one error as JSON in
// Sentry's shape or the simple one (message, type, value, stack as text,
// level, environment, release, user, tags, extra, fingerprint).
func (s *Server) handleProjectEvent(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p, err := s.store.GetProject(ctx, actorID(ctx), idParam(r, "id"))
	if err != nil {
		s.fail(w, err, "project")
		return
	}
	s.reportTo(w, r, p)
}

// handleReport is POST /report: an app reporting its own error through a
// Pocket Core link (errors:write) goes to the project named after it,
// made on its first error. With a person's own token, project_id says where.
func (s *Server) handleReport(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	uid := actorID(ctx)
	app := linkApp(ctx)
	if app == "" {
		pid, _ := toInt64(r.URL.Query().Get("project_id"))
		p, err := s.store.GetProject(ctx, uid, pid)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "say which project (project_id); Pocket apps reporting through Pocket Core get one of their own")
			return
		}
		s.reportTo(w, r, p)
		return
	}
	p, err := s.store.ProjectBySource(ctx, uid, app)
	if errors.Is(err, store.ErrNotFound) {
		name := s.appName(ctx, app)
		p = &store.Project{UserID: uid, Name: name, Platform: "go", Source: app, AppSlug: app, NotifyNew: true, NotifyAgain: true}
		if err = s.store.CreateProject(ctx, p); errors.Is(err, store.ErrConflict) {
			p, err = s.store.ProjectBySource(ctx, uid, app)
		}
	}
	if err != nil {
		s.fail(w, err, "project")
		return
	}
	s.reportTo(w, r, p)
}

func (s *Server) reportTo(w http.ResponseWriter, r *http.Request, p *store.Project) {
	b, err := s.readBody(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "could not read the body")
		return
	}
	var m map[string]any
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if err := dec.Decode(&m); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	ev := intake.FromMap(m)
	if len(ev.Data.Exceptions) == 0 && ev.Data.Message == "" {
		writeErr(w, http.StatusBadRequest, "send a message, or an error's type, value or stack")
		return
	}
	res, err := s.ingest(r.Context(), p, ev)
	if err != nil {
		if errors.Is(err, errTooMany) {
			writeErr(w, http.StatusTooManyRequests, err.Error())
			return
		}
		s.fail(w, err, "keep event")
		return
	}
	out := map[string]any{"id": ev.EventID, "project_id": p.ID, "outcome": res.Outcome}
	if res.Issue != nil {
		out["issue_id"] = res.Issue.ID
		out["url"] = s.link("/issues/" + itoa(res.Issue.ID))
	}
	writeJSON(w, http.StatusCreated, out)
}

// appName is what Pocket Core calls an app ("notes" → "Pocket Notes").
func (s *Server) appName(ctx context.Context, slug string) string {
	if s.core.Configured() {
		if res, err := s.core.Resolve(ctx, slug); err == nil && res.Name != "" {
			return res.Name
		}
	}
	if slug == "" {
		return "An app"
	}
	return "Pocket " + strings.ToUpper(slug[:1]) + slug[1:]
}

// ingestBase is where apps send errors: the public address, or the one on
// this box when that differs (another container reaching this one).
func (s *Server) ingestBase() string {
	return firstNonEmpty(s.cfg.IngestURL, s.cfg.BaseURL)
}

// dsn is a project's Sentry DSN at an address.
func dsn(base string, p *store.Project) string {
	if base == "" {
		base = "http://localhost:8080"
	}
	u, err := url.Parse(base)
	if err != nil {
		return ""
	}
	return u.Scheme + "://" + p.PublicKey + "@" + u.Host + strings.TrimRight(u.Path, "/") + "/" + itoa(p.ID)
}

func toInt64(v string) (int64, bool) {
	var n int64
	for _, c := range v {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int64(c-'0')
	}
	return n, v != ""
}

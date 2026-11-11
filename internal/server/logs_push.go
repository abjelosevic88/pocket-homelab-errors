package server

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/abjelosevic88/pocket-homelab-errors/internal/intake"
	"github.com/abjelosevic88/pocket-homelab-errors/internal/logs"
	"github.com/abjelosevic88/pocket-homelab-errors/internal/store"
)

// ---- lines a phone or an app sends ----
//
// Besides what the box writes, a device (the Pocket Homelab app on a
// phone) may send its own lines: POST /logs/push with a source slug and a
// batch of lines. They are kept as a source of kind and group "device"
// beside the containers and the journal, the same way: levels, repeating
// messages, counts, search, the source's keep setting, secrets hidden,
// tidied with the rest.

// reSourceSlug is what a device or an app sending without a Core link is
// called (report's ?source=, push's "source").
var reSourceSlug = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,47}$`)

// validSlug reports whether v may name a source ("docker" stands for the
// watched containers' projects).
func validSlug(v string) bool { return reSourceSlug.MatchString(v) && v != "docker" }

const (
	pushMaxLines  = 1000
	pushMaxBytes  = 1 << 20
	pushMaxFields = 40
	pushAhead     = 10 * time.Minute // a line further ahead than this is now: the phone's clock is off
)

type pushLine struct {
	At      string         `json:"at"`
	Level   string         `json:"level"`
	Message string         `json:"message"`
	Fields  map[string]any `json:"fields"`
	Msg     string         `json:"msg"`   // the same, as other senders spell it
	Attrs   map[string]any `json:"attrs"` // the same
}

// POST /logs/push: {source, name, lines: [{at, level, message, fields}]}
// as JSON, or text (one line each; iPhone/BSD syslog read with its time and
// process) and NDJSON with ?source= and ?name= (or X-Source-Label): what
// pocket-logs.sh sends.
func (s *Server) handleLogsPush(w http.ResponseWriter, r *http.Request) {
	if !s.logsAdmin(w, r) {
		return
	}
	ctx := r.Context()
	st := s.logSettings(ctx)
	if !st.On {
		writeErr(w, http.StatusConflict, "logs are off on this box")
		return
	}
	b, err := io.ReadAll(http.MaxBytesReader(nil, r.Body, pushMaxBytes))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeErr(w, http.StatusRequestEntityTooLarge, "send at most 1 MB at a time")
			return
		}
		writeErr(w, http.StatusBadRequest, "could not read the body")
		return
	}
	q := r.URL.Query()
	var in struct {
		Source string     `json:"source"`
		Name   string     `json:"name"`
		Lines  []pushLine `json:"lines"`
	}
	ct := strings.ToLower(r.Header.Get("Content-Type"))
	text := !strings.Contains(ct, "json") || strings.Contains(ct, "ndjson")
	if !text {
		dec := json.NewDecoder(bytes.NewReader(b))
		dec.UseNumber()
		if err := dec.Decode(&in); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid json")
			return
		}
	}
	// the query names the source for text (the script), and may for JSON
	in.Source = firstNonEmpty(strings.TrimSpace(in.Source), strings.TrimSpace(q.Get("source")))
	in.Name = firstNonEmpty(strings.TrimSpace(in.Name), q.Get("name"), q.Get("label"), r.Header.Get("X-Source-Label"))
	if !validSlug(in.Source) {
		writeErr(w, http.StatusBadRequest, "source is 2 to 48 lowercase letters, digits and dashes (not docker)")
		return
	}
	now := time.Now()
	var parsed []logs.Entry // text: one line each (an iPhone's or a Mac's syslog keeps its time)
	if text {
		parsed = logs.ParsePush(b, ct, now, pushMaxLines+1)
		if len(parsed) > pushMaxLines {
			writeErr(w, http.StatusRequestEntityTooLarge, "send at most 1000 lines at a time")
			return
		}
	} else if len(in.Lines) > pushMaxLines {
		writeErr(w, http.StatusRequestEntityTooLarge, "send at most 1000 lines at a time")
		return
	}
	name := clip(strings.TrimSpace(in.Name), 100)
	src, err := s.logs.Ensure(ctx, logs.SourceInfo{Kind: "device", Name: in.Source, Label: firstNonEmpty(name, in.Source), Group: "device"})
	if err != nil {
		s.fail(w, err, "log source")
		return
	}
	key := logs.SourceKey(src.Kind, src.Name)
	oldest := now.AddDate(0, 0, -st.KeepDays)
	batch := make([]logs.Entry, 0, len(in.Lines)+len(parsed))
	dropped := 0
	// a clock ahead is now; a line older than the logs keep is dropped
	when := func(at time.Time) (time.Time, bool) {
		switch {
		case at.After(now.Add(pushAhead)):
			return now, true
		case at.Before(oldest):
			return at, false
		}
		return at, true
	}
	for _, e := range parsed {
		at, ok := when(e.At)
		if !ok {
			dropped++
			continue
		}
		e.At, e.Source = at, key
		batch = append(batch, e)
	}
	for _, l := range in.Lines {
		at, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(l.At))
		if err != nil {
			at = now
		}
		at, ok := when(at)
		if !ok {
			dropped++
			continue
		}
		if e, ok := pushEntry(l, at, key); ok {
			batch = append(batch, e)
		} else {
			dropped++
		}
	}
	taken, _, dup, err := s.logs.WriteNew(ctx, batch)
	if err != nil {
		s.fail(w, err, "keep log lines")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"accepted": taken, "dropped": dropped + dup, "duplicates": dup, "source_id": src.ID})
}

//go:embed pocket-logs.sh
var forwardScript []byte

// GET /pocket-logs.sh: the script that sends a computer's lines (or an
// iPhone's system log, through a Mac it is paired with) to Logs. No secrets in it.
func (s *Server) handleForwardScript(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.Header().Set("Content-Disposition", `inline; filename="pocket-logs.sh"`)
	_, _ = w.Write(forwardScript)
}

// pushEntry turns a sent line into one kept: the level it says (else the
// one its words give), secrets hidden in the message and the fields.
func pushEntry(l pushLine, at time.Time, key string) (logs.Entry, bool) {
	msg := strings.TrimRight(firstNonEmpty(l.Message, l.Msg), "\r\n")
	if len(l.Fields) == 0 {
		l.Fields = l.Attrs
	}
	if strings.TrimSpace(msg) == "" {
		return logs.Entry{}, false
	}
	e := logs.Entry{At: at, Source: key, Level: logs.ParseLevel(l.Level), Msg: logs.Scrub(clip(msg, 4000)), Bytes: len(msg)}
	if e.Level == 0 {
		e.Level = logs.Parse(msg, 0).Level
	}
	for _, k := range sortedFieldKeys(l.Fields) {
		if len(e.Attrs) >= pushMaxFields {
			break
		}
		v := fieldText(l.Fields[k])
		k = clip(strings.TrimSpace(k), 60)
		if k == "" || v == "" {
			continue
		}
		if e.Attrs == nil {
			e.Attrs = map[string]string{}
		}
		if isSecret(k) {
			e.Attrs[k] = filtered
		} else {
			e.Attrs[k] = logs.Scrub(clip(v, 1000))
		}
		e.Bytes += len(k) + len(v)
	}
	return e, true
}

func fieldText(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case json.Number:
		return t.String()
	case bool:
		return strconv.FormatBool(t)
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func sortedFieldKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// keepSentryLogs keeps the "log" items of a Sentry envelope (an app with a
// Sentry SDK and its logs switched on) as a device source: the project's
// own source name when it has one (so its issues show these lines), else
// project-<id>. Only when Logs is on and the project is an admin's.
func (s *Server) keepSentryLogs(ctx context.Context, p *store.Project, items []intake.EnvelopeItem) {
	if s.logs == nil {
		return
	}
	var entries []logs.Entry
	now := time.Now()
	for _, it := range items {
		if it.Type == "log" {
			entries = append(entries, logs.SentryLogs(it.Payload, now)...)
		}
	}
	if len(entries) == 0 || !s.logSettings(ctx).On {
		return
	}
	if u, err := s.store.GetUser(ctx, p.UserID); err != nil || u.Role != "admin" {
		return
	}
	name := p.Source
	if !validSlug(name) {
		name = "project-" + strconv.FormatInt(p.ID, 10)
	}
	src, err := s.logs.Ensure(ctx, logs.SourceInfo{Kind: "device", Name: name, Label: p.Name, Group: "device"})
	if err != nil {
		return
	}
	key := logs.SourceKey(src.Kind, src.Name)
	for i := range entries {
		entries[i].Source = key
	}
	if _, _, _, err := s.logs.WriteNew(ctx, entries); err != nil {
		s.log.Warn("keeping an app's log lines failed", "project", p.ID, "err", err)
	}
}

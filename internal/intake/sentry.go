// Package intake turns what apps send — Sentry events and envelopes, plain
// JSON, stack traces as text, container log lines — into one shape, and
// decides which events are the same problem.
package intake

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/abjelosevic88/pocket-homelab-errors/internal/store"
)

// Event is an error on its way in: the event itself plus what the sender
// said about it.
type Event struct {
	EventID     string
	At          time.Time
	Level       string
	Platform    string
	Release     string
	Environment string
	Who         string
	Data        store.EventData
}

// ErrNoEvent means a Sentry envelope had nothing to keep (only sessions,
// transactions, client reports).
var ErrNoEvent = errors.New("nothing to keep")

// ParseSentryEvent reads one Sentry event (the store endpoint's body or an
// envelope's event item).
func ParseSentryEvent(b []byte) (*Event, error) {
	var m map[string]any
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("not a JSON event: %w", err)
	}
	return FromMap(m), nil
}

// FromMap reads an event from decoded JSON in Sentry's shape (also what
// the plain JSON API takes: message, type, value, stack as text).
func FromMap(m map[string]any) *Event {
	ev := &Event{
		EventID:     cleanID(str(m["event_id"])),
		At:          parseTime(m["timestamp"]),
		Level:       normLevel(str(m["level"])),
		Platform:    clip(str(m["platform"]), 40),
		Release:     clip(str(m["release"]), 200),
		Environment: clip(str(m["environment"]), 80),
	}
	d := &ev.Data
	d.Logger = clip(str(m["logger"]), 200)
	d.Transaction = clip(firstOf(str(m["transaction"]), str(m["culprit"])), 300)
	d.ServerName = clip(str(m["server_name"]), 200)
	if sdk, ok := m["sdk"].(map[string]any); ok {
		d.SDK = strings.TrimSpace(str(sdk["name"]) + " " + str(sdk["version"]))
	}
	// the message: a string, {formatted, message, params} or logentry
	switch v := m["message"].(type) {
	case string:
		d.Message = v
	case map[string]any:
		d.Message = firstOf(str(v["formatted"]), str(v["message"]))
	}
	if le, ok := m["logentry"].(map[string]any); ok && d.Message == "" {
		d.Message = firstOf(str(le["formatted"]), str(le["message"]))
	}
	d.Message = clip(d.Message, 8000)
	// exceptions: {values: [...]} or [...]
	var exs []any
	switch v := m["exception"].(type) {
	case map[string]any:
		exs, _ = v["values"].([]any)
	case []any:
		exs = v
	}
	for _, x := range exs {
		xm, ok := x.(map[string]any)
		if !ok {
			continue
		}
		e := store.Exception{Type: clip(str(xm["type"]), 200), Value: clip(str(xm["value"]), 4000), Module: clip(str(xm["module"]), 200)}
		if mech, ok := xm["mechanism"].(map[string]any); ok {
			e.Mechanism = clip(str(mech["type"]), 80)
			if h, ok := mech["handled"].(bool); ok {
				e.Handled = &h
			}
		}
		if st, ok := xm["stacktrace"].(map[string]any); ok {
			e.Frames = frames(st["frames"])
		}
		e.Frames = trimFrames(e.Frames)
		if e.Type != "" || e.Value != "" || len(e.Frames) > 0 {
			d.Exceptions = append(d.Exceptions, e)
		}
	}
	// a stack for a message event lives on the crashed/current thread
	if len(d.Exceptions) == 0 {
		if th, ok := m["threads"].(map[string]any); ok {
			vals, _ := th["values"].([]any)
			for _, t := range vals {
				tm, _ := t.(map[string]any)
				if tm == nil {
					continue
				}
				if st, ok := tm["stacktrace"].(map[string]any); ok && (truthy(tm["crashed"]) || truthy(tm["current"]) || len(vals) == 1) {
					if fr := trimFrames(frames(st["frames"])); len(fr) > 0 {
						d.Exceptions = append(d.Exceptions, store.Exception{Frames: fr})
						break
					}
				}
			}
		}
	}
	// the plain JSON API: type, value and a stack as text
	if len(d.Exceptions) == 0 {
		typ, val, stack := clip(str(m["type"]), 200), clip(str(m["value"]), 4000), str(m["stack"])
		if typ != "" || stack != "" {
			e := store.Exception{Type: typ, Value: val}
			if stack != "" {
				pt, pv, fr := ParseStack(stack)
				e.Frames = trimFrames(fr)
				if e.Type == "" {
					e.Type = pt
				}
				if e.Value == "" {
					e.Value = pv
				}
			}
			if e.Type == "" && e.Value == "" {
				e.Value = d.Message
			}
			d.Exceptions = append(d.Exceptions, e)
		}
	}
	// breadcrumbs: {values: [...]} or [...]
	var crumbs []any
	switch v := m["breadcrumbs"].(type) {
	case map[string]any:
		crumbs, _ = v["values"].([]any)
	case []any:
		crumbs = v
	}
	if len(crumbs) > 100 {
		crumbs = crumbs[len(crumbs)-100:]
	}
	for _, c := range crumbs {
		cm, ok := c.(map[string]any)
		if !ok {
			continue
		}
		bc := store.Breadcrumb{Type: clip(str(cm["type"]), 40), Category: clip(str(cm["category"]), 80), Level: normLevel(str(cm["level"])),
			Message: clip(str(cm["message"]), 1000)}
		if t := parseTime(cm["timestamp"]); !t.IsZero() && cm["timestamp"] != nil {
			bc.At = store.Stamp(t)
		}
		if str(cm["level"]) == "" {
			bc.Level = ""
		}
		if dm, ok := cm["data"].(map[string]any); ok {
			bc.Data = flat(dm, 12, 300)
		}
		d.Breadcrumbs = append(d.Breadcrumbs, bc)
	}
	// the request
	if rq, ok := m["request"].(map[string]any); ok {
		r := &store.Request{Method: clip(strings.ToUpper(str(rq["method"])), 10), URL: clip(str(rq["url"]), 2000)}
		switch q := rq["query_string"].(type) {
		case string:
			r.Query = clip(q, 2000)
		case map[string]any, []any:
			r.Query = clip(pairsText(pairs(q)), 2000)
		}
		r.Headers = pairs(rq["headers"])
		if len(r.Headers) > 40 {
			r.Headers = r.Headers[:40]
		}
		switch v := rq["data"].(type) {
		case string:
			r.Data = clip(v, 4000)
		case nil:
		default:
			b, _ := json.Marshal(v)
			r.Data = clip(string(b), 4000)
		}
		if r.Method != "" || r.URL != "" {
			d.Request = r
		}
	}
	// the app's user
	if u, ok := m["user"].(map[string]any); ok {
		d.User = map[string]string{}
		for _, k := range []string{"id", "email", "username", "ip_address", "name", "segment"} {
			if v := clip(str(u[k]), 200); v != "" {
				d.User[k] = v
			}
		}
		if len(d.User) == 0 {
			d.User = nil
		}
		ev.Who = firstOf(d.User["id"], d.User["email"], d.User["username"], d.User["ip_address"])
	}
	// tags: {k: v} or [[k, v], …]
	d.Tags = pairs(m["tags"])
	if len(d.Tags) > 50 {
		d.Tags = d.Tags[:50]
	}
	// contexts: {browser: {name, version}, os: {...}}
	if cx, ok := m["contexts"].(map[string]any); ok {
		d.Contexts = map[string]store.KeyValues{}
		for name, v := range cx {
			vm, ok := v.(map[string]any)
			if !ok || len(d.Contexts) >= 12 {
				continue
			}
			kv := store.KeyValues{}
			for _, k := range sortedKeys(vm) {
				if k == "type" || len(kv) >= 20 {
					continue
				}
				if s := scalar(vm[k]); s != "" {
					kv = append(kv, [2]string{k, clip(s, 300)})
				}
			}
			if len(kv) > 0 {
				d.Contexts[name] = kv
			}
		}
		if len(d.Contexts) == 0 {
			d.Contexts = nil
		}
	}
	if ex, ok := m["extra"].(map[string]any); ok && len(ex) > 0 {
		b, _ := json.Marshal(ex)
		if len(b) <= 16000 {
			d.Extra = ex
		} else {
			d.Extra = map[string]any{"note": "too large to keep (" + strconv.Itoa(len(b)) + " bytes)"}
		}
	}
	if fp, ok := m["fingerprint"].([]any); ok {
		for _, f := range fp {
			if s := str(f); s != "" && len(d.Fingerprint) < 10 {
				d.Fingerprint = append(d.Fingerprint, clip(s, 200))
			}
		}
	}
	if ll, ok := m["log_lines"].([]any); ok {
		for _, l := range ll {
			if s := str(l); s != "" && len(d.LogLines) < 40 {
				d.LogLines = append(d.LogLines, clip(s, 2000))
			}
		}
	}
	return ev
}

// EnvelopeItem is one item of a Sentry envelope.
type EnvelopeItem struct {
	Type    string
	Payload []byte
}

// ReadEnvelope splits a Sentry envelope into its header and items (as far
// as they read well, with the error that stopped it).
func ReadEnvelope(b []byte) (map[string]any, []EnvelopeItem, error) {
	r := bufio.NewReaderSize(bytes.NewReader(b), 64<<10)
	head, err := readLine(r)
	if err != nil {
		return nil, nil, fmt.Errorf("empty envelope")
	}
	var h map[string]any
	if err := json.Unmarshal(head, &h); err != nil {
		return nil, nil, fmt.Errorf("bad envelope header")
	}
	var items []EnvelopeItem
	for {
		line, err := readLine(r)
		if err != nil {
			break
		}
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var ih map[string]any
		if err := json.Unmarshal(line, &ih); err != nil {
			return h, items, fmt.Errorf("bad item header")
		}
		var payload []byte
		if n, ok := ih["length"]; ok {
			size := int(num(n))
			if size < 0 || size > len(b) {
				return h, items, fmt.Errorf("bad item length")
			}
			payload = make([]byte, size)
			if _, err := ioReadFull(r, payload); err != nil {
				return h, items, fmt.Errorf("short item")
			}
			_, _ = r.ReadByte() // the newline after it
		} else {
			payload, _ = readLine(r)
		}
		items = append(items, EnvelopeItem{Type: str(ih["type"]), Payload: payload})
	}
	return h, items, nil
}

// ParseEnvelope reads a Sentry envelope and returns its error events
// (transactions, sessions and the like are skipped) and the DSN its header
// carries, if any.
func ParseEnvelope(b []byte) ([]*Event, string, error) {
	h, items, rerr := ReadEnvelope(b)
	if h == nil {
		return nil, "", rerr
	}
	dsn := str(h["dsn"])
	headerID := cleanID(str(h["event_id"]))
	var out []*Event
	for _, it := range items {
		if it.Type != "event" && it.Type != "error" {
			continue
		}
		ev, err := ParseSentryEvent(it.Payload)
		if err != nil {
			continue
		}
		if ev.EventID == "" {
			ev.EventID = headerID
		}
		out = append(out, ev)
	}
	if len(out) == 0 {
		if rerr != nil {
			return nil, dsn, rerr
		}
		return nil, dsn, ErrNoEvent
	}
	return out, dsn, rerr
}

func readLine(r *bufio.Reader) ([]byte, error) {
	line, err := r.ReadBytes('\n')
	if err != nil && len(line) == 0 {
		return nil, err
	}
	return bytes.TrimRight(line, "\r\n"), nil
}

func ioReadFull(r *bufio.Reader, buf []byte) (int, error) {
	n := 0
	for n < len(buf) {
		k, err := r.Read(buf[n:])
		n += k
		if err != nil {
			return n, err
		}
	}
	return n, nil
}

// SentryKey finds the project key in an X-Sentry-Auth header (or the
// Authorization header some SDKs use): "Sentry sentry_key=…, sentry_version=7".
func SentryKey(header string) string {
	h := strings.TrimSpace(header)
	if len(h) >= 7 && strings.EqualFold(h[:7], "sentry ") {
		h = h[7:]
	}
	for _, part := range strings.Split(h, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if ok && strings.TrimSpace(k) == "sentry_key" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// KeyFromDSN is the key in a DSN (https://KEY@host/ID).
func KeyFromDSN(dsn string) string {
	i := strings.Index(dsn, "://")
	if i < 0 {
		return ""
	}
	rest := dsn[i+3:]
	at := strings.Index(rest, "@")
	if at < 0 {
		return ""
	}
	key, _, _ := strings.Cut(rest[:at], ":")
	return key
}

// ---- small JSON helpers ----

func frames(v any) []store.Frame {
	list, _ := v.([]any)
	out := []store.Frame{}
	for _, f := range list {
		fm, ok := f.(map[string]any)
		if !ok {
			continue
		}
		fr := store.Frame{Function: clip(str(fm["function"]), 300), Module: clip(firstOf(str(fm["module"]), str(fm["package"])), 300),
			File: clip(firstOf(str(fm["filename"]), str(fm["abs_path"])), 500), Line: int(num(fm["lineno"])), Col: int(num(fm["colno"]))}
		if v, ok := fm["in_app"].(bool); ok {
			fr.InApp = v
		} else {
			fr.InApp = guessInApp(fr.File, fr.Module)
		}
		fr.ContextLine = clip(str(fm["context_line"]), 400)
		fr.PreContext = lines(fm["pre_context"], 5)
		fr.PostContext = lines(fm["post_context"], 5)
		out = append(out, fr)
	}
	return out
}

func lines(v any, n int) []string {
	list, _ := v.([]any)
	if len(list) > n {
		list = list[len(list)-n:]
	}
	var out []string
	for _, l := range list {
		out = append(out, clip(str(l), 400))
	}
	return out
}

// trimFrames keeps the 60 frames closest to the error (the stack's end).
func trimFrames(f []store.Frame) []store.Frame {
	if len(f) > 60 {
		return f[len(f)-60:]
	}
	return f
}

// guessInApp says a frame is the app's own code unless it plainly belongs
// to a library, the runtime or the browser.
func guessInApp(file, module string) bool {
	p := strings.ToLower(file + " " + module)
	for _, lib := range []string{"node_modules", "/usr/lib/", "/usr/local/lib/", "site-packages", "dist-packages", "/go/pkg/mod/", "/runtime/", "runtime.",
		"<anonymous>", "node:internal", "webpack/bootstrap", "chrome-extension://", "moz-extension://", "/pkg/mod/", "go/src/", "net/http.", "golang.org/"} {
		if strings.Contains(p, lib) {
			return false
		}
	}
	return file != "" || module != ""
}

func str(v any) string {
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case json.Number:
		return t.String()
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(t)
	}
	return ""
}

func scalar(v any) string {
	switch v.(type) {
	case string, json.Number, float64, bool:
		return str(v)
	case nil:
		return ""
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func num(v any) float64 {
	switch t := v.(type) {
	case json.Number:
		f, _ := t.Float64()
		return f
	case float64:
		return t
	case int:
		return float64(t)
	case int64:
		return float64(t)
	case string:
		f, _ := strconv.ParseFloat(t, 64)
		return f
	}
	return 0
}

func truthy(v any) bool { b, _ := v.(bool); return b }

func firstOf(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

func clip(s string, n int) string {
	s = strings.ToValidUTF8(s, "")
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8Start(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

func utf8Start(b byte) bool { return b&0xC0 != 0x80 }

func cleanID(s string) string {
	s = strings.ToLower(strings.ReplaceAll(s, "-", ""))
	if len(s) != 32 {
		return ""
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return ""
		}
	}
	return s
}

// NewEventID is a fresh 32-hex event id.
func NewEventID() string { return store.NewKey() }

func normLevel(l string) string {
	switch strings.ToLower(strings.TrimSpace(l)) {
	case "fatal", "critical", "crit", "panic", "emerg", "alert":
		return "fatal"
	case "warning", "warn":
		return "warning"
	case "info", "notice", "log":
		return "info"
	case "debug", "trace":
		return "debug"
	}
	return "error"
}

// parseTime reads a Sentry timestamp: seconds since 1970 (a number or a
// numeric string) or an ISO time; anything else, or a time far off, is now.
func parseTime(v any) time.Time {
	now := time.Now().UTC()
	var t time.Time
	switch x := v.(type) {
	case json.Number, float64:
		f := num(x)
		if f > 1e12 { // milliseconds
			f /= 1000
		}
		sec, frac := math.Modf(f)
		t = time.Unix(int64(sec), int64(frac*1e9)).UTC()
	case string:
		if f, err := strconv.ParseFloat(x, 64); err == nil {
			return parseTime(f)
		}
		for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999", "2006-01-02T15:04:05", "2006-01-02 15:04:05"} {
			if p, err := time.Parse(layout, x); err == nil {
				t = p.UTC()
				break
			}
		}
	}
	if t.IsZero() || t.Before(now.AddDate(-1, 0, 0)) || t.After(now.Add(10*time.Minute)) {
		return now
	}
	return t
}

// pairs reads {k: v} or [[k, v], …] as ordered pairs.
func pairs(v any) [][2]string {
	var out [][2]string
	switch t := v.(type) {
	case map[string]any:
		for _, k := range sortedKeys(t) {
			if s := scalar(t[k]); s != "" {
				out = append(out, [2]string{clip(k, 100), clip(s, 500)})
			}
		}
	case []any:
		for _, p := range t {
			if pa, ok := p.([]any); ok && len(pa) == 2 {
				if k, v := str(pa[0]), scalar(pa[1]); k != "" {
					out = append(out, [2]string{clip(k, 100), clip(v, 500)})
				}
			}
		}
	}
	return out
}

func pairsText(p [][2]string) string {
	parts := make([]string, 0, len(p))
	for _, kv := range p {
		parts = append(parts, kv[0]+"="+kv[1])
	}
	return strings.Join(parts, "&")
}

func flat(m map[string]any, n, size int) map[string]string {
	out := map[string]string{}
	for _, k := range sortedKeys(m) {
		if len(out) >= n {
			break
		}
		if s := scalar(m[k]); s != "" {
			out[clip(k, 60)] = clip(s, size)
		}
	}
	return out
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

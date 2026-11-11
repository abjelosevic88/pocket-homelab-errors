// Package logs keeps every line this box's containers and services write:
// its own SQLite file (logs.db, next to the main one) so a busy day of
// lines never slows the issues down, with full-text search, counts per
// five minutes and per hour that outlive the lines, the messages that repeat
// (patterns) and the web traffic Caddy logged.
package logs

import (
	"encoding/json"
	"hash/fnv"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Levels, worst last. 0 is never stored.
const (
	Debug = 1
	Info  = 2
	Warn  = 3
	Error = 4
	Fatal = 5
)

// LevelNames maps a level to its word and back.
var LevelNames = map[int]string{Debug: "debug", Info: "info", Warn: "warning", Error: "error", Fatal: "fatal"}

// ParseLevel reads a level word ("warn", "ERROR", "crit", "5"…); 0 when it is none.
func ParseLevel(s string) int {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "trace", "debug", "dbug", "dbg", "verbose", "d":
		return Debug
	case "info", "inf", "information", "notice", "i", "log":
		return Info
	case "warn", "warning", "wrn", "w":
		return Warn
	case "error", "err", "eror", "e", "severe":
		return Error
	case "fatal", "crit", "critical", "panic", "emerg", "emergency", "alert", "f", "dpanic":
		return Fatal
	}
	return 0
}

// Entry is one line as kept.
type Entry struct {
	At     time.Time
	Source string // "kind/name", see Source
	Level  int
	Stream int // 1 stdout, 2 stderr, 0 unknown
	Msg    string
	Attrs  map[string]string
	Web    *Access // a request Caddy (or another proxy) logged
	Bytes  int     // the line as written, for the counts
}

// Access is one web request from an access log.
type Access struct {
	Host   string
	Method string
	Path   string
	Status int
	Ms     float64
}

var (
	reLogfmt   = regexp.MustCompile(`(\w[\w.-]*)=("(?:[^"\\]|\\.)*"|\S*)`)
	reStamp    = regexp.MustCompile(`^\[?\d{4}[-/]\d\d[-/]\d\d[T ]\d\d:\d\d:\d\d(?:[.,]\d+)?(?:Z|[+-]\d\d:?\d\d| ?UTC| ?GMT)?\]?[\s:|-]*`)
	reStamp2   = regexp.MustCompile(`^\[?(?:\d\d:\d\d:\d\d(?:[.,]\d+)?|[A-Z][a-z]{2} [ \d]\d \d\d:\d\d:\d\d|\d\d/[A-Z][a-z]{2}/\d{4}:\d\d:\d\d:\d\d [+-]\d{4})\]?[\s:|-]*`)
	reLevelTok = regexp.MustCompile(`(?i)(?:^|[\s\[(<|:])(trace|debug|dbug|dbg|info|inf|notice|warn|warning|wrn|error|err|eror|fatal|crit|critical|panic|emerg)(?:[\s\]):>|,]|$)`)
	reKlog     = regexp.MustCompile(`^([IWEF])\d{4} \d\d:\d\d:\d\d\.\d+\s+\d+ (\S+:\d+)\] (.*)$`)
	reSecret   = regexp.MustCompile(`(?i)\b(password|passwd|pwd|secret|client_secret|token|access_token|refresh_token|api[_-]?key|apikey|authorization|x-api-key|private_key)\b(["']?\s*[:=]\s*["']?)(?:bearer\s+|basic\s+)?([^\s"'&,;}]{3,})`)
	reBearer   = regexp.MustCompile(`(?i)\b(bearer|basic)\s+[A-Za-z0-9._~+/=-]{12,}`)
)

// Scrub hides what looks like a password, token or key.
func Scrub(s string) string {
	if !strings.ContainsAny(s, "=:") && !strings.Contains(strings.ToLower(s), "bearer") {
		return s
	}
	s = reSecret.ReplaceAllString(s, "$1$2••••")
	return reBearer.ReplaceAllString(s, "$1 ••••")
}

// Parse reads one line a program wrote: Go's slog (text and JSON), other
// JSON logs, logfmt, klog, Caddy's access log and plain text with a level
// word near its start. prev is the level of the line before in the same
// source (an indented line goes on with it: stack traces).
func Parse(text string, prev int) Entry {
	text = strings.TrimRight(text, "\r\n")
	e := Entry{Bytes: len(text)}
	t := strings.TrimSpace(text)
	switch {
	case strings.HasPrefix(t, "{") && strings.HasSuffix(t, "}"):
		if parseJSON(t, &e) {
			break
		}
		e.Msg = t
		e.Level = Info
	case strings.Contains(t, "msg=") && (strings.Contains(t, "level=") || strings.Contains(t, "lvl=")):
		parseLogfmt(t, &e)
	default:
		if m := reKlog.FindStringSubmatch(t); m != nil {
			e.Level = map[string]int{"I": Info, "W": Warn, "E": Error, "F": Fatal}[m[1]]
			e.Msg = m[3]
			e.Attrs = map[string]string{"caller": m[2]}
			break
		}
		body := stripStamps(t)
		e.Msg = body
		head := body
		if len(head) > 48 {
			head = head[:48]
		}
		if m := reLevelTok.FindStringSubmatch(head); m != nil {
			e.Level = ParseLevel(m[1])
		}
		switch {
		case strings.HasPrefix(body, "panic: ") || strings.HasPrefix(body, "fatal error: ") || strings.HasPrefix(body, "Traceback (most recent call last)"):
			e.Level = max(e.Level, Error)
		case e.Level == 0 && prev >= Warn && (strings.HasPrefix(text, " ") || strings.HasPrefix(text, "\t") || strings.HasPrefix(body, "goroutine ") || strings.HasPrefix(body, "at ") || strings.HasPrefix(body, "created by ")):
			e.Level = prev // the rest of a stack
		}
	}
	if e.Level == 0 {
		e.Level = Info
	}
	e.Msg = Scrub(clip(e.Msg, 4000))
	for k, v := range e.Attrs {
		e.Attrs[k] = Scrub(v)
	}
	return e
}

func stripStamps(t string) string {
	for i := 0; i < 2; i++ {
		n := reStamp.ReplaceAllString(t, "")
		n = reStamp2.ReplaceAllString(n, "")
		if n == t {
			break
		}
		t = n
	}
	return strings.TrimSpace(t)
}

var jsonSkip = map[string]bool{"time": true, "ts": true, "timestamp": true, "@timestamp": true, "t": true, "level": true, "severity": true, "lvl": true,
	"levelname": true, "msg": true, "message": true, "@message": true, "@level": true, "v": true, "pid": true, "hostname": true}

func parseJSON(t string, e *Entry) bool {
	var m map[string]any
	if json.Unmarshal([]byte(t), &m) != nil {
		return false
	}
	lv := firstOf(str(m["level"]), str(m["severity"]), str(m["lvl"]), str(m["levelname"]), str(m["@level"]))
	e.Level = ParseLevel(lv)
	if e.Level == 0 {
		if n, ok := m["level"].(float64); ok { // pino / bunyan: 10…60
			switch {
			case n >= 60:
				e.Level = Fatal
			case n >= 50:
				e.Level = Error
			case n >= 40:
				e.Level = Warn
			case n >= 30:
				e.Level = Info
			default:
				e.Level = Debug
			}
		}
	}
	e.Msg = firstOf(str(m["msg"]), str(m["message"]), str(m["@message"]))
	// Caddy's access log: one request
	if req, ok := m["request"].(map[string]any); ok && m["status"] != nil && (strings.HasPrefix(str(m["logger"]), "http.log.access") || e.Msg == "handled request") {
		a := &Access{Host: str(req["host"]), Method: str(req["method"]), Path: str(req["uri"])}
		a.Status = int(num(m["status"]))
		a.Ms = math.Round(num(m["duration"])*1000*10) / 10
		if i := strings.LastIndexByte(a.Host, ':'); i > 0 && !strings.Contains(a.Host[i:], "]") {
			a.Host = a.Host[:i]
		}
		e.Web = a
		path := a.Path
		if len(path) > 300 {
			path = path[:300] + "…"
		}
		e.Msg = a.Method + " " + a.Host + Scrub(path) + " → " + strconv.Itoa(a.Status) + " · " + fmtMs(a.Ms)
		e.Attrs = map[string]string{"method": a.Method, "host": a.Host, "uri": Scrub(clip(a.Path, 1000)), "status": strconv.Itoa(a.Status),
			"ms": strconv.FormatFloat(a.Ms, 'f', -1, 64), "ip": firstOf(str(req["client_ip"]), str(req["remote_ip"])), "proto": str(req["proto"])}
		if sz := num(m["size"]); sz > 0 {
			e.Attrs["size"] = strconv.FormatInt(int64(sz), 10)
		}
		if h, ok := req["headers"].(map[string]any); ok {
			if ua := headerVal(h["User-Agent"]); ua != "" {
				e.Attrs["ua"] = clip(ua, 200)
			}
		}
		if a.Status >= 500 {
			e.Level = max(e.Level, Error)
		} else if e.Level >= Error {
			e.Level = Info // Caddy writes 4xx and timeouts as errors; the request itself is fine to keep at info
		}
		return true
	}
	e.Attrs = map[string]string{}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if jsonSkip[k] || len(e.Attrs) >= 24 {
			continue
		}
		if s := scalar(m[k]); s != "" {
			e.Attrs[k] = clip(s, 400)
		}
	}
	if e.Msg == "" {
		for _, k := range []string{"error", "err", "event"} {
			if v := e.Attrs[k]; v != "" {
				e.Msg = v
				delete(e.Attrs, k)
				break
			}
		}
	}
	if e.Msg == "" {
		e.Msg = t
		e.Attrs = nil
	}
	return true
}

func headerVal(v any) string {
	switch x := v.(type) {
	case []any:
		if len(x) > 0 {
			return str(x[0])
		}
	case string:
		return x
	}
	return ""
}

func parseLogfmt(t string, e *Entry) {
	e.Attrs = map[string]string{}
	for _, m := range reLogfmt.FindAllStringSubmatch(t, -1) {
		k, v := m[1], m[2]
		if strings.HasPrefix(v, `"`) {
			var u string
			if json.Unmarshal([]byte(v), &u) == nil {
				v = u
			}
		}
		switch k {
		case "level", "lvl":
			e.Level = ParseLevel(v)
		case "msg":
			e.Msg = v
		case "time", "ts", "t":
		default:
			if len(e.Attrs) < 24 {
				e.Attrs[k] = clip(v, 400)
			}
		}
	}
	if e.Msg == "" {
		e.Msg = t
	}
}

func fmtMs(ms float64) string {
	switch {
	case ms >= 10000:
		return strconv.FormatFloat(ms/1000, 'f', 0, 64) + " s"
	case ms >= 1000:
		return strconv.FormatFloat(ms/1000, 'f', 1, 64) + " s"
	case ms >= 10:
		return strconv.FormatFloat(ms, 'f', 0, 64) + " ms"
	}
	return strconv.FormatFloat(ms, 'f', 1, 64) + " ms"
}

// ---- patterns: the message with its values taken out ----

var (
	reUUID   = regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`)
	reIP     = regexp.MustCompile(`\b\d{1,3}(?:\.\d{1,3}){3}(?::\d+)?\b|\[[0-9a-fA-F:]+\](?::\d+)?`)
	reHex    = regexp.MustCompile(`(?i)\b(?:0x)?[0-9a-f]*\d[0-9a-f]*[a-f][0-9a-f]*\b|\b(?:0x)?[0-9a-f]*[a-f][0-9a-f]*\d[0-9a-f]*\b`)
	reEmail  = regexp.MustCompile(`\b[\w.+-]+@[\w-]+\.[\w.-]+\b`)
	reDate   = regexp.MustCompile(`\b\d{4}-\d\d-\d\d(?:[T ]\d\d:\d\d(?::\d\d(?:\.\d+)?)?(?:Z|[+-]\d\d:?\d\d)?)?\b|\b\d\d:\d\d:\d\d(?:\.\d+)?\b`)
	reNum    = regexp.MustCompile(`[-+]?\b\d+(?:[.,]\d+)?(?:\s?(?:ms|µs|us|ns|s|m|h|kb|mb|gb|kib|mib|gib|b|%))?\b`)
	reQuoted = regexp.MustCompile(`"[^"]{24,}"|'[^']{24,}'`)
	reQuery  = regexp.MustCompile(`\?[^\s]*`)
	reSpaces = regexp.MustCompile(`\s+`)
)

// Template is a message with its values (numbers, ids, addresses, times)
// taken out, so the same message with other values counts as one.
func Template(msg string) string {
	t := msg
	if len(t) > 600 {
		t = t[:600]
	}
	t = reUUID.ReplaceAllString(t, "‹id›")
	t = reEmail.ReplaceAllString(t, "‹email›")
	t = reDate.ReplaceAllString(t, "‹time›")
	t = reIP.ReplaceAllString(t, "‹ip›")
	t = reQuery.ReplaceAllString(t, "?‹…›")
	t = reQuoted.ReplaceAllString(t, "\"‹…›\"")
	t = reHex.ReplaceAllStringFunc(t, func(s string) string {
		if len(s) >= 6 {
			return "‹hex›"
		}
		return s
	})
	t = reNum.ReplaceAllString(t, "‹n›")
	t = reSpaces.ReplaceAllString(strings.TrimSpace(t), " ")
	if len(t) > 300 {
		t = t[:300] + "…"
	}
	return t
}

// webTemplate is a request without its values: method, site, path with
// ids taken out, and the status class.
func webTemplate(a *Access) string {
	p := a.Path
	if i := strings.IndexByte(p, '?'); i >= 0 {
		p = p[:i]
	}
	segs := strings.Split(p, "/")
	for i, s := range segs {
		if s == "" {
			continue
		}
		if reUUID.MatchString(s) || strings.ContainsAny(s, "0123456789") && (len(s) > 2 || strings.Trim(s, "0123456789") == "") || len(s) > 32 {
			segs[i] = "‹…›"
		}
	}
	p = strings.Join(segs, "/")
	if len(p) > 120 {
		p = p[:120] + "…"
	}
	return a.Method + " " + a.Host + p + " → " + strconv.Itoa(a.Status/100) + "xx"
}

// PatternID is a stable id for a template in a source.
func PatternID(sourceID int64, template string) int64 {
	h := fnv.New64a()
	h.Write([]byte(strconv.FormatInt(sourceID, 10)))
	h.Write([]byte{0})
	h.Write([]byte(template))
	return int64(h.Sum64() >> 1) // positive
}

// ---- small helpers ----

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && cut > n-4 && !utf8Start(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

func utf8Start(b byte) bool { return b&0xC0 != 0x80 }

func firstOf(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

func str(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(x)
	}
	return ""
}

func num(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case string:
		f, _ := strconv.ParseFloat(x, 64)
		return f
	}
	return 0
}

func scalar(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string, float64, bool:
		return str(x)
	default:
		b, _ := json.Marshal(x)
		return string(b)
	}
}

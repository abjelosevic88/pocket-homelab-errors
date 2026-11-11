package logs

import (
	"bufio"
	"bytes"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Lines sent from outside the box (a phone, a laptop, an app): through
// POST /logs/push as text (one line each), NDJSON or JSON, or as the log
// items of a Sentry envelope. They go to the "device" group.

// PushLine is one line as it is sent in JSON.
type PushLine struct {
	At      any            `json:"at"` // unix s or ms, or RFC 3339; empty = now
	Time    any            `json:"time"`
	Level   string         `json:"level"`
	Msg     string         `json:"msg"`
	Message string         `json:"message"`
	Attrs   map[string]any `json:"attrs"`
}

// reSyslog is what idevicesyslog (and BSD syslog) writes:
// "Nov 11 21:03:04 iPhone SpringBoard(FrontBoard)[57] <Notice>: message".
var reSyslog = regexp.MustCompile(`^([A-Z][a-z]{2} [ \d]\d \d\d:\d\d:\d\d)(?:\.\d+)? (\S+) ([^\[(\s]+)(?:\(([^)]*)\))?\[(\d+)\](?: <(\w+)>)?: ?(.*)$`)

var syslogLevels = map[string]int{"fault": Error, "error": Error, "err": Error, "crit": Fatal, "alert": Fatal, "emerg": Fatal,
	"warning": Warn, "warn": Warn, "notice": Info, "default": Info, "info": Info, "debug": Debug}

// ParseText reads one pushed text line; prev is the level of the line before.
func ParseText(line string, now time.Time, prev int) Entry {
	if m := reSyslog.FindStringSubmatch(line); m != nil {
		e := Entry{Bytes: len(line), Level: Info}
		if lv, ok := syslogLevels[strings.ToLower(m[6])]; ok {
			e.Level = lv
		} else if p := Parse(m[7], 0); p.Level != Info {
			e.Level = p.Level
		}
		e.Msg = Scrub(clip(m[3]+": "+m[7], 4000))
		e.Attrs = map[string]string{"process": m[3], "pid": m[5], "device": m[2]}
		if m[4] != "" {
			e.Attrs["library"] = m[4]
		}
		e.At = syslogTime(m[1], now)
		return e
	}
	e := Parse(line, prev)
	e.At = now
	return e
}

// syslogTime reads "Nov 11 21:03:04" as local time this year (last year
// when that would be in the future).
func syslogTime(s string, now time.Time) time.Time {
	t, err := time.ParseInLocation("Jan _2 15:04:05 2006", s+" "+strconv.Itoa(now.Year()), time.Local)
	if err != nil {
		return now
	}
	if t.After(now.Add(24 * time.Hour)) {
		t = t.AddDate(-1, 0, 0)
	}
	return t
}

// ParsePush reads a pushed body: JSON ({"lines": [...]} or [...]), NDJSON
// or plain text, at most max lines.
func ParsePush(body []byte, contentType string, now time.Time, max int) []Entry {
	body = bytes.TrimSpace(body)
	var out []Entry
	ct := strings.ToLower(contentType)
	if strings.Contains(ct, "json") && !strings.Contains(ct, "ndjson") && (bytes.HasPrefix(body, []byte("{")) || bytes.HasPrefix(body, []byte("["))) {
		var doc struct {
			Lines []PushLine `json:"lines"`
		}
		var arr []PushLine
		if bytes.HasPrefix(body, []byte("[")) {
			if json.Unmarshal(body, &arr) == nil {
				doc.Lines = arr
			}
		} else if json.Unmarshal(body, &doc) != nil || doc.Lines == nil {
			var one PushLine
			if json.Unmarshal(body, &one) == nil && (one.Msg != "" || one.Message != "") {
				doc.Lines = []PushLine{one}
			}
		}
		for _, l := range doc.Lines {
			if len(out) >= max {
				break
			}
			if e, ok := pushEntry(l, now); ok {
				out = append(out, e)
			}
		}
		return out
	}
	sc := bufio.NewScanner(bytes.NewReader(body))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	prev := 0
	for sc.Scan() && len(out) < max {
		t := strings.TrimRight(sc.Text(), "\r")
		if strings.TrimSpace(t) == "" {
			continue
		}
		if strings.HasPrefix(t, "{") && strings.Contains(ct, "ndjson") {
			var l PushLine
			if json.Unmarshal([]byte(t), &l) == nil && (l.Msg != "" || l.Message != "") {
				if e, ok := pushEntry(l, now); ok {
					out = append(out, e)
					prev = e.Level
					continue
				}
			}
		}
		e := ParseText(t, now, prev)
		prev = e.Level
		out = append(out, e)
	}
	return out
}

func pushEntry(l PushLine, now time.Time) (Entry, bool) {
	msg := firstOf(l.Msg, l.Message)
	if strings.TrimSpace(msg) == "" {
		return Entry{}, false
	}
	var e Entry
	if l.Level == "" && len(l.Attrs) == 0 {
		e = Parse(msg, 0)
	} else {
		e = Entry{Level: ParseLevel(l.Level), Msg: Scrub(clip(msg, 4000)), Bytes: len(msg)}
		if e.Level == 0 {
			e.Level = Info
		}
	}
	if len(l.Attrs) > 0 {
		if e.Attrs == nil {
			e.Attrs = map[string]string{}
		}
		for k, v := range l.Attrs {
			if len(e.Attrs) >= 24 {
				break
			}
			if s := scalar(v); s != "" {
				e.Attrs[k] = Scrub(clip(s, 400))
			}
		}
	}
	e.At = when(firstAny(l.At, l.Time), now)
	return e, true
}

func firstAny(v ...any) any {
	for _, x := range v {
		if x != nil && x != "" {
			return x
		}
	}
	return nil
}

// when reads unix seconds or milliseconds (number or text) or RFC 3339;
// anything far off (an hour ahead, a week back) is now.
func when(v any, now time.Time) time.Time {
	var t time.Time
	switch x := v.(type) {
	case float64:
		t = unixAny(x)
	case string:
		if f, err := strconv.ParseFloat(x, 64); err == nil {
			t = unixAny(f)
		} else if p, err := time.Parse(time.RFC3339Nano, x); err == nil {
			t = p
		}
	}
	if t.IsZero() || t.After(now.Add(time.Hour)) || t.Before(now.Add(-7*24*time.Hour)) {
		return now
	}
	return t
}

func unixAny(f float64) time.Time {
	if f > 1e14 { // µs
		return time.UnixMicro(int64(f))
	}
	if f > 1e11 { // ms
		return time.UnixMilli(int64(f))
	}
	sec := int64(f)
	return time.Unix(sec, int64((f-float64(sec))*1e9))
}

// SentryLogs reads the payload of a Sentry envelope's "log" item:
// {"items": [{"timestamp", "level", "body", "attributes": {k: {"value", "type"}}}]}.
func SentryLogs(payload []byte, now time.Time) []Entry {
	var doc struct {
		Items []struct {
			Timestamp  any                        `json:"timestamp"`
			Level      string                     `json:"level"`
			Body       string                     `json:"body"`
			Attributes map[string]json.RawMessage `json:"attributes"`
		} `json:"items"`
	}
	if json.Unmarshal(payload, &doc) != nil {
		return nil
	}
	out := make([]Entry, 0, len(doc.Items))
	for _, it := range doc.Items {
		if strings.TrimSpace(it.Body) == "" {
			continue
		}
		e := Entry{Level: ParseLevel(it.Level), Msg: Scrub(clip(it.Body, 4000)), Bytes: len(it.Body), At: when(it.Timestamp, now)}
		if e.Level == 0 {
			e.Level = Info
		}
		e.Attrs = map[string]string{}
		for k, raw := range it.Attributes {
			if len(e.Attrs) >= 24 || strings.HasPrefix(k, "sentry.sdk") || k == "sentry.message.template" {
				continue
			}
			var a struct {
				Value any `json:"value"`
			}
			if json.Unmarshal(raw, &a) == nil {
				if s := scalar(a.Value); s != "" {
					e.Attrs[strings.TrimPrefix(k, "sentry.")] = Scrub(clip(s, 400))
				}
			}
		}
		out = append(out, e)
	}
	return out
}

// SourceSlug makes a pushed source name safe: letters, digits, . _ - and :.
func SourceSlug(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '_', r == '-', r == ':':
			b.WriteRune(r)
		case r == ' ':
			b.WriteRune('-')
		}
		if b.Len() >= 64 {
			break
		}
	}
	return strings.Trim(b.String(), "-")
}

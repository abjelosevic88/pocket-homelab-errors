package intake

import (
	"encoding/json"
	"regexp"
	"strings"
	"time"

	"github.com/abjelosevic88/pocket-homelab-errors/internal/store"
)

// LogReader turns a container's log lines into events: Go's structured
// logs (text and JSON) at error level, other programs' JSON logs, Go panics,
// Python tracebacks and plain lines that say ERROR. One reader per
// container; it keeps a panic or traceback until it has all of it.
type LogReader struct {
	Container string
	Warnings  bool // count warnings too, not only errors

	block   []string
	blockAt time.Time
	kind    string   // panic | traceback
	before  []string // the lines before the block started
	recent  []string
}

var (
	reLogfmt    = regexp.MustCompile(`(\w[\w.-]*)=("(?:[^"\\]|\\.)*"|\S*)`)
	rePlainErr  = regexp.MustCompile(`(?i)(^|[\s\[(|:])(error|err|fatal|critical|crit|panic|emerg|alert|severe)([\s\]):|]|$)`)
	rePlainWarn = regexp.MustCompile(`(?i)(^|[\s\[(|:])(warn|warning)([\s\]):|]|$)`)
	reKlog      = regexp.MustCompile(`^([EFW])\d{4} \d\d:\d\d:\d\d\.\d+\s+\d+ (\S+:\d+)\] (.*)$`)
	reStackish  = regexp.MustCompile(`^(\s|goroutine |created by |\[|-> |panic:|fatal error:|\S+\(.*\)$|\S+\.\S+\(\.\.\.\)$|exit status|$)`)
	reHTTPPanic = regexp.MustCompile(`^(?:\S+ \S+ )?http: panic serving \S+: (.*)$`)
)

// Line reads one line (its time from the log) and returns the events it
// completes: usually none or one, two when a block ends because a new error
// line starts.
func (r *LogReader) Line(at time.Time, line string) []*Event {
	line = strings.TrimRight(line, "\r\n")
	var out []*Event
	if r.kind != "" {
		if r.continues(line) {
			r.block = append(r.block, line)
			if r.kind == "traceback" && r.tracebackDone(line) || len(r.block) >= 300 {
				out = append(out, r.flush())
			}
			r.remember(line)
			return out
		}
		out = append(out, r.flush())
	}
	t := strings.TrimSpace(line)
	switch {
	case strings.HasPrefix(t, "panic: ") || strings.HasPrefix(t, "fatal error: ") || reHTTPPanic.MatchString(t):
		r.kind, r.block, r.blockAt, r.before = "panic", []string{t}, at, append([]string{}, r.recent...)
	case strings.HasPrefix(t, "Traceback (most recent call last)"):
		r.kind, r.block, r.blockAt, r.before = "traceback", []string{t}, at, append([]string{}, r.recent...)
	default:
		if ev := r.single(at, t); ev != nil {
			out = append(out, ev)
		}
	}
	r.remember(line)
	return out
}

// Flush ends a block that waits for more lines (the logs went quiet).
func (r *LogReader) Flush() *Event {
	if r.kind == "" {
		return nil
	}
	return r.flush()
}

// Pending reports whether a panic or traceback is still being read.
func (r *LogReader) Pending() bool { return r.kind != "" }

func (r *LogReader) remember(line string) {
	r.recent = append(r.recent, clip(line, 2000))
	if len(r.recent) > 12 {
		r.recent = r.recent[len(r.recent)-12:]
	}
}

func (r *LogReader) continues(line string) bool {
	if r.kind == "traceback" {
		return strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") || strings.TrimSpace(line) == "" ||
			strings.HasPrefix(line, "During handling") || strings.HasPrefix(line, "The above exception") || strings.HasPrefix(line, "Traceback") ||
			len(r.block) > 0 && !r.tracebackHasError()
	}
	return reStackish.MatchString(line) && !looksLikeLogEntry(line)
}

func (r *LogReader) tracebackHasError() bool {
	last := r.block[len(r.block)-1]
	return !strings.HasPrefix(last, " ") && !strings.HasPrefix(last, "Traceback") && strings.TrimSpace(last) != ""
}

func (r *LogReader) tracebackDone(line string) bool {
	return !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") && strings.TrimSpace(line) != "" &&
		!strings.HasPrefix(line, "Traceback") && !strings.HasPrefix(line, "During handling") && !strings.HasPrefix(line, "The above exception")
}

func looksLikeLogEntry(line string) bool {
	t := strings.TrimSpace(line)
	return strings.HasPrefix(t, "time=") || strings.HasPrefix(t, "{\"") || strings.HasPrefix(t, "level=")
}

func (r *LogReader) flush() *Event {
	text := strings.Join(r.block, "\n")
	kind := r.kind
	at := r.blockAt
	r.kind, r.block = "", nil
	typ, value, frames := ParseStack(text)
	if m := reHTTPPanic.FindStringSubmatch(strings.TrimSpace(strings.SplitN(text, "\n", 2)[0])); m != nil {
		typ, value = "panic", m[1]
	}
	if typ == "" {
		typ = map[string]string{"panic": "panic", "traceback": "Exception"}[kind]
	}
	level := "fatal"
	if kind == "traceback" {
		level = "error"
	}
	ev := &Event{EventID: NewEventID(), At: at, Level: level, Platform: map[string]string{"panic": "go", "traceback": "python"}[kind]}
	ev.Data.Logger = r.Container
	ev.Data.Exceptions = []store.Exception{{Type: typ, Value: clip(value, 4000), Frames: trimFrames(frames), Mechanism: "log"}}
	ev.Data.LogLines = r.context(r.block, text)
	return ev
}

// context is the lines before it and the block itself, for the issue page.
func (r *LogReader) context(_ []string, text string) []string {
	out := append([]string{}, r.before...)
	if len(out) > 5 {
		out = out[len(out)-5:]
	}
	for _, l := range strings.Split(text, "\n") {
		if len(out) >= 40 {
			break
		}
		out = append(out, clip(l, 2000))
	}
	return out
}

// single reads one line on its own.
func (r *LogReader) single(at time.Time, t string) *Event {
	if t == "" {
		return nil
	}
	var level, msg, errText, logger string
	attrs := map[string]string{}
	switch {
	case strings.HasPrefix(t, "{"):
		var m map[string]any
		if json.Unmarshal([]byte(t), &m) != nil {
			return nil
		}
		level = strings.ToLower(firstOf(str(m["level"]), str(m["severity"]), str(m["lvl"]), str(m["levelname"])))
		msg = firstOf(str(m["msg"]), str(m["message"]), str(m["error"]))
		errText = firstOf(str(m["err"]), str(m["error"]))
		logger = firstOf(str(m["logger"]), str(m["name"]))
		for k, v := range m {
			switch k {
			case "time", "ts", "timestamp", "level", "severity", "lvl", "levelname", "msg", "message", "err", "error", "logger", "name", "caller":
				continue
			}
			if s := scalar(v); s != "" && len(s) <= 300 && len(attrs) < 20 {
				attrs[k] = s
			}
		}
	case strings.Contains(t, "level=") && strings.Contains(t, "msg="):
		for _, m := range reLogfmt.FindAllStringSubmatch(t, -1) {
			k, v := m[1], m[2]
			if strings.HasPrefix(v, `"`) {
				if u, err := unquote(v); err == nil {
					v = u
				}
			}
			switch k {
			case "level":
				level = strings.ToLower(v)
			case "msg":
				msg = v
			case "err", "error":
				errText = v
			case "time":
			default:
				if len(attrs) < 20 && len(v) <= 300 {
					attrs[k] = v
				}
			}
		}
	default:
		if m := reKlog.FindStringSubmatch(t); m != nil {
			level = map[string]string{"E": "error", "F": "fatal", "W": "warning"}[m[1]]
			msg, logger = m[3], m[2]
			break
		}
		switch {
		case rePlainErr.MatchString(t):
			level = "error"
		case rePlainWarn.MatchString(t):
			level = "warning"
		default:
			return nil
		}
		msg = stripStamp(t)
	}
	lv := normLevel(level)
	if level == "" {
		return nil
	}
	// a client that left before the answer is not an error of the app
	if e := strings.TrimSpace(errText); e == "context canceled" || msg == "context canceled" {
		return nil
	}
	if lv == "info" || lv == "debug" || lv == "warning" && !r.Warnings {
		return nil
	}
	if msg == "" {
		msg = errText
		errText = ""
	}
	if msg == "" {
		return nil
	}
	ev := &Event{EventID: NewEventID(), At: at, Level: lv, Platform: "log"}
	d := &ev.Data
	d.Logger = firstOf(logger, r.Container)
	d.Message = clip(msg, 4000)
	if errText != "" && errText != msg {
		d.Message = clip(msg+": "+errText, 4000)
	}
	if method, p := attrs["method"], firstOf(attrs["path"], attrs["uri"], attrs["url"]); method != "" && p != "" {
		d.Request = &store.Request{Method: strings.ToUpper(method), URL: clip(p, 2000)}
	}
	for _, k := range sortedKeys(anyMap(attrs)) {
		d.Tags = append(d.Tags, [2]string{k, attrs[k]})
	}
	d.LogLines = append(append([]string{}, lastN(r.recent, 5)...), clip(t, 2000))
	// group by what the line says, not by the values in it
	d.Fingerprint = []string{"log", d.Logger, Normalize(msg), Normalize(firstLine(errText))}
	return ev
}

func anyMap(m map[string]string) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func lastN(s []string, n int) []string {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}

func unquote(v string) (string, error) {
	var s string
	err := json.Unmarshal([]byte(v), &s)
	return s, err
}

var reStamp = regexp.MustCompile(`^\[?\d{4}[-/]\d\d[-/]\d\d[T ]\d\d:\d\d:\d\d(?:[.,]\d+)?(?:Z|[+-]\d\d:?\d\d)?\]?\s*`)

// stripStamp drops a leading date and time a program wrote itself.
func stripStamp(s string) string { return strings.TrimSpace(reStamp.ReplaceAllString(s, "")) }

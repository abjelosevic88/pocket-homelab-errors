package intake

import (
	"crypto/sha1"
	"encoding/hex"
	"net/url"
	"regexp"
	"strings"

	"github.com/abjelosevic88/pocket-homelab-errors/internal/store"
)

var (
	reUUID   = regexp.MustCompile(`\b[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}\b`)
	reAddr   = regexp.MustCompile(`\b0x[0-9a-fA-F]+\b`)
	reHex    = regexp.MustCompile(`\b[0-9a-fA-F]*[0-9][0-9a-fA-F]*[a-fA-F][0-9a-fA-F]*\b|\b[0-9a-fA-F]*[a-fA-F][0-9a-fA-F]*[0-9][0-9a-fA-F]*\b`)
	reEmail  = regexp.MustCompile(`[\w.+-]+@[\w-]+\.[\w.-]+`)
	reQuoted = regexp.MustCompile(`"[^"\n]{0,120}"|'[^'\n]{0,120}'|` + "`[^`\n]{0,120}`")
	reNum    = regexp.MustCompile(`\d+(\.\d+)?`)
	reSpace  = regexp.MustCompile(`\s+`)
)

// Normalize takes the parts of a message that change every time (numbers,
// ids, addresses, quoted values, emails) out, so the same problem reads
// the same.
func Normalize(s string) string {
	s = reUUID.ReplaceAllString(s, "<id>")
	s = reAddr.ReplaceAllString(s, "<addr>")
	s = reEmail.ReplaceAllString(s, "<email>")
	s = reQuoted.ReplaceAllString(s, "<s>")
	s = reHex.ReplaceAllStringFunc(s, func(h string) string {
		if len(h) >= 8 {
			return "<hex>"
		}
		return h
	})
	s = reNum.ReplaceAllString(s, "<n>")
	s = reSpace.ReplaceAllString(strings.TrimSpace(s), " ")
	if len(s) > 400 {
		s = s[:400]
	}
	return s
}

// Main is the exception that was raised last (Sentry lists causes first),
// nil for a message.
func Main(d *store.EventData) *store.Exception {
	if len(d.Exceptions) == 0 {
		return nil
	}
	return &d.Exceptions[len(d.Exceptions)-1]
}

// Fingerprint decides which issue an event belongs to: the sender's own
// fingerprint when it gave one ("{{ default }}" stands for ours), else the
// error's type and the app's own frames (without line numbers, so a small
// change elsewhere in the file keeps it the same issue), else the type and
// the message with the changing parts taken out.
func Fingerprint(project int64, d *store.EventData) string {
	parts := defaultParts(d)
	if len(d.Fingerprint) > 0 {
		var own []string
		for _, f := range d.Fingerprint {
			if strings.TrimSpace(f) == "{{ default }}" || strings.TrimSpace(f) == "{{default}}" {
				own = append(own, parts...)
			} else {
				own = append(own, f)
			}
		}
		parts = append([]string{"custom"}, own...)
	}
	h := sha1.Sum([]byte(strings.Join(parts, "\x1f")))
	return hex.EncodeToString(h[:])
}

func defaultParts(d *store.EventData) []string {
	if e := Main(d); e != nil {
		parts := []string{"exception", e.Type}
		var fr []store.Frame
		for _, f := range e.Frames {
			if f.InApp {
				fr = append(fr, f)
			}
		}
		if len(fr) == 0 && len(e.Frames) > 0 {
			fr = e.Frames
			if len(fr) > 5 {
				fr = fr[len(fr)-5:]
			}
		}
		if len(fr) > 10 {
			fr = fr[len(fr)-10:]
		}
		for _, f := range fr {
			parts = append(parts, frameKey(f))
		}
		if len(fr) == 0 || e.Type == "" || e.Type == "Error" || e.Type == "panic" {
			parts = append(parts, Normalize(firstLine(e.Value)))
		}
		return parts
	}
	return []string{"message", d.Logger, Normalize(firstLine(d.Message))}
}

// frameKey is a frame without what moves between builds: line numbers,
// bundle hashes in file names, anonymous function numbers.
func frameKey(f store.Frame) string {
	fn := reNum.ReplaceAllString(f.Function, "")
	return f.Module + "|" + bundleName(baseName(f.File)) + "|" + fn
}

// Title is how an issue is called: "TypeError: x is undefined", or the
// message's first line.
func Title(d *store.EventData) string {
	if e := Main(d); e != nil {
		v := firstLine(e.Value)
		switch {
		case e.Type != "" && v != "":
			return clip(e.Type+": "+v, 300)
		case e.Type != "":
			return clip(e.Type, 300)
		case v != "":
			return clip(v, 300)
		}
	}
	if m := firstLine(d.Message); m != "" {
		return clip(m, 300)
	}
	return "An error without a message"
}

// Type is the main exception's type ("" for a message).
func Type(d *store.EventData) string {
	if e := Main(d); e != nil {
		return e.Type
	}
	return ""
}

// Culprit is where it went wrong: the request or job it happened in, else
// the app's own frame closest to the error, else the logger, else the page.
func Culprit(d *store.EventData) string {
	if d.Transaction != "" {
		return clip(d.Transaction, 300)
	}
	if e := Main(d); e != nil {
		for i := len(e.Frames) - 1; i >= 0; i-- {
			f := e.Frames[i]
			if f.InApp && (f.Function != "" || f.File != "") {
				return clip(frameText(f), 300)
			}
		}
		if n := len(e.Frames); n > 0 {
			return clip(frameText(e.Frames[n-1]), 300)
		}
	}
	if d.Logger != "" {
		return d.Logger
	}
	if d.Request != nil && d.Request.URL != "" {
		if u, err := url.Parse(d.Request.URL); err == nil && u.Path != "" {
			return clip(strings.TrimSpace(d.Request.Method+" "+u.Path), 300)
		}
	}
	return ""
}

func frameText(f store.Frame) string {
	fn := f.Function
	if f.Module != "" && fn != "" && !strings.Contains(fn, f.Module) {
		fn = shortModule(f.Module) + "." + fn
	}
	file := baseName(f.File)
	switch {
	case fn != "" && file != "":
		return fn + " (" + file + ")"
	case fn != "":
		return fn
	}
	return file
}

func shortModule(m string) string {
	if i := strings.LastIndex(m, "/"); i >= 0 {
		return m[i+1:]
	}
	return m
}

// bundleName drops a build hash from a file name ("index-Dfr4Rbqi.js" →
// "index.js"), so a new build keeps the same issues.
func bundleName(file string) string {
	m := reBundle.FindStringSubmatchIndex(file)
	if m == nil {
		return file
	}
	hash := file[m[2]:m[3]]
	if !strings.ContainsAny(hash, "0123456789") {
		return file
	}
	return file[:m[0]] + file[m[4]:m[5]]
}

var reBundle = regexp.MustCompile(`[-.]([A-Za-z0-9_]{6,})(\.m?[jt]sx?)$`)

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}

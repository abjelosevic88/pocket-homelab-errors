package intake

import (
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/abjelosevic88/pocket-homelab-errors/internal/store"
)

var (
	// JavaScript (V8): "    at fn (https://x/app.js:10:5)", "    at https://x/app.js:10:5", "    at async fn (file:1:2)"
	reV8      = regexp.MustCompile(`^\s*at (?:async )?(?:(.+?) \((.+?):(\d+)(?::(\d+))?\)|(.+?):(\d+)(?::(\d+))?)\s*$`)
	reV8Plain = regexp.MustCompile(`^\s*at (?:async )?(.+?) \((native|<anonymous>|index \d+)\)\s*$`)
	// Firefox / Safari: "fn@https://x/app.js:10:5", "@https://x/app.js:10:5"
	reGecko = regexp.MustCompile(`^\s*([^@\s]*)@(.+?):(\d+)(?::(\d+))?\s*$`)
	// Python: '  File "/app/x.py", line 10, in fn'
	rePyFile = regexp.MustCompile(`^\s*File "(.+?)", line (\d+)(?:, in (.+))?\s*$`)
	// Go: "main.handler(0xc000…)" then "\t/app/main.go:42 +0x1d"
	reGoFunc = regexp.MustCompile(`^(?:created by )?([\w./*()\[\]-]+?\.[\w.*()\[\]-]+)(?:\(.*\))?(?: in goroutine \d+)?\s*$`)
	reGoArgs = regexp.MustCompile(`\(([^*()][^()]*)?\)$`)
	reGoFile = regexp.MustCompile(`^\s+(\S+\.go):(\d+)(?: \+0x[0-9a-f]+)?\s*$`)
	// chi's pretty stack: " -> pkg.fn" / " ->   /path/file.go:42" or "    pkg.fn" / "      /path/file.go:42"
	reChiLine = regexp.MustCompile(`^\s*(->)?\s*(\S+\.go):(\d+)\s*$`)
	// Java / Kotlin: "at com.x.Y.z(Y.java:10)"
	reJava = regexp.MustCompile(`^\s*at ([\w$.<>]+)\(([^:()]+)(?::(\d+))?\)\s*$`)
	// "TypeError: message" — a type is one word, maybe dotted
	reTypeHead = regexp.MustCompile(`^([A-Za-z_$][\w$.]*(?:Error|Exception|Panic|Fault|Warning|Failure)?|panic|fatal error):\s*(.*)$`)
)

// ParseStack reads a stack trace written as text — JavaScript (Chrome,
// Firefox, Safari), Python, Go (a panic, debug.Stack or chi's), Java — and
// returns the error's type and message (when the text starts with them) and
// the frames outermost first, the failing one last.
func ParseStack(text string) (typ, value string, out []store.Frame) {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(lines) == 0 {
		return "", "", nil
	}
	// Python tracebacks: frames outermost first, the error on the last line
	if strings.HasPrefix(strings.TrimSpace(lines[0]), "Traceback (most recent call last)") || anyMatch(lines, rePyFile) {
		var fr []store.Frame
		for i := 0; i < len(lines); i++ {
			m := rePyFile.FindStringSubmatch(lines[i])
			if m == nil {
				continue
			}
			n, _ := strconv.Atoi(m[2])
			f := store.Frame{File: m[1], Line: n, Function: m[3], InApp: guessInApp(m[1], "")}
			if i+1 < len(lines) && !rePyFile.MatchString(lines[i+1]) && strings.HasPrefix(lines[i+1], "    ") {
				f.ContextLine = clip(strings.TrimSpace(lines[i+1]), 400)
			}
			fr = append(fr, f)
		}
		for i := len(lines) - 1; i >= 0; i-- {
			l := strings.TrimSpace(lines[i])
			if l == "" || strings.HasPrefix(l, "File ") || strings.HasPrefix(l, "^") {
				continue
			}
			typ, value = splitHead(l)
			break
		}
		return typ, value, fr
	}
	// Go: frames innermost first in pairs (function, then file:line)
	if goLike(lines) {
		var fr []store.Frame
		head := ""
		for i := 0; i < len(lines); i++ {
			l := lines[i]
			if head == "" && strings.TrimSpace(l) != "" && !strings.HasPrefix(l, "goroutine ") {
				head = strings.TrimSpace(l)
			}
			if m := reChiLine.FindStringSubmatch(l); m != nil && i > 0 && !strings.HasPrefix(l, "\t") {
				fn := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(lines[i-1]), "->"))
				n, _ := strconv.Atoi(m[3])
				fr = append(fr, goFrame(fn, m[2], n, m[1] == "->"))
				continue
			}
			if m := reGoFile.FindStringSubmatch(l); m != nil && i > 0 {
				fn := strings.TrimPrefix(strings.TrimSpace(lines[i-1]), "created by ")
				fn, _, _ = strings.Cut(fn, " in goroutine ")
				fn = reGoArgs.ReplaceAllString(fn, "")
				n, _ := strconv.Atoi(m[2])
				fr = append(fr, goFrame(fn, m[1], n, false))
			}
		}
		reverse(fr)
		if head != "" && !reGoFunc.MatchString(head) {
			typ, value = splitHead(head)
			if strings.HasPrefix(head, "panic: ") {
				typ, value = "panic", strings.TrimSpace(strings.TrimPrefix(head, "panic: "))
				value = strings.TrimSuffix(value, " [recovered]")
			}
		}
		return typ, value, fr
	}
	// JavaScript / Java: the message first, frames innermost first
	var fr []store.Frame
	var head []string
	for _, l := range lines {
		switch {
		case reV8.MatchString(l):
			m := reV8.FindStringSubmatch(l)
			f := store.Frame{Function: m[1], File: m[2], Line: atoi(m[3]), Col: atoi(m[4])}
			if m[2] == "" {
				f.File, f.Line, f.Col = m[5], atoi(m[6]), atoi(m[7])
			}
			f.InApp = guessInApp(f.File, "")
			fr = append(fr, f)
		case reV8Plain.MatchString(l):
			m := reV8Plain.FindStringSubmatch(l)
			fr = append(fr, store.Frame{Function: m[1], File: m[2]})
		case reJava.MatchString(l):
			m := reJava.FindStringSubmatch(l)
			mod, fn := splitLast(m[1], ".")
			fr = append(fr, store.Frame{Function: fn, Module: mod, File: m[2], Line: atoi(m[3]), InApp: !javaLib(mod)})
		case reGecko.MatchString(l) && !strings.Contains(l, " "):
			m := reGecko.FindStringSubmatch(l)
			fr = append(fr, store.Frame{Function: m[1], File: m[2], Line: atoi(m[3]), Col: atoi(m[4]), InApp: guessInApp(m[2], "")})
		case len(fr) == 0:
			head = append(head, l)
		}
	}
	reverse(fr)
	if len(head) > 0 {
		typ, value = splitHead(strings.TrimSpace(head[0]))
		if len(head) > 1 {
			value = strings.TrimSpace(value + "\n" + strings.Join(head[1:], "\n"))
		}
	}
	return typ, clip(value, 4000), fr
}

func goFrame(fn, file string, line int, inApp bool) store.Frame {
	mod, name := goSplit(fn)
	f := store.Frame{Function: name, Module: mod, File: file, Line: line}
	f.InApp = inApp || guessInApp(file, mod) && !strings.HasPrefix(mod, "runtime") && !strings.Contains(mod, "go-chi/")
	return f
}

// goSplit splits "github.com/x/y/pkg.(*T).Method" into the package and the rest.
func goSplit(fn string) (string, string) {
	slash := strings.LastIndex(fn, "/")
	dot := strings.Index(fn[slash+1:], ".")
	if dot < 0 {
		return "", fn
	}
	return fn[:slash+1+dot], fn[slash+2+dot:]
}

func goLike(lines []string) bool {
	for _, l := range lines {
		if strings.HasPrefix(l, "goroutine ") || reGoFile.MatchString(l) || strings.HasPrefix(strings.TrimSpace(l), "-> ") && strings.Contains(l, ".go:") {
			return true
		}
	}
	return false
}

func anyMatch(lines []string, re *regexp.Regexp) bool {
	for _, l := range lines {
		if re.MatchString(l) {
			return true
		}
	}
	return false
}

// splitHead reads "TypeError: x is undefined" as the type and the message;
// a line that does not look like that is all message.
func splitHead(l string) (string, string) {
	if m := reTypeHead.FindStringSubmatch(l); m != nil && len(m[1]) <= 80 {
		return m[1], m[2]
	}
	return "", l
}

func splitLast(s, sep string) (string, string) {
	i := strings.LastIndex(s, sep)
	if i < 0 {
		return "", s
	}
	return s[:i], s[i+1:]
}

func javaLib(mod string) bool {
	for _, p := range []string{"java.", "javax.", "sun.", "kotlin.", "android.", "jdk.", "org.springframework."} {
		if strings.HasPrefix(mod, p) {
			return true
		}
	}
	return false
}

func atoi(s string) int { n, _ := strconv.Atoi(s); return n }

func reverse(f []store.Frame) {
	for i, j := 0, len(f)-1; i < j; i, j = i+1, j-1 {
		f[i], f[j] = f[j], f[i]
	}
}

// baseName is a frame's file without its folders (and a URL's query).
func baseName(f string) string {
	f, _, _ = strings.Cut(f, "?")
	return path.Base(strings.ReplaceAll(f, "\\", "/"))
}

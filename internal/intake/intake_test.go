package intake

import (
	"strings"
	"testing"
	"time"
)

func TestParseStackJS(t *testing.T) {
	typ, val, fr := ParseStack("TypeError: Cannot read properties of undefined (reading 'price')\n" +
		"    at lineTotal (https://shop.test/assets/money-1b2c3d4e.js:12:20)\n" +
		"    at CartRow (https://shop.test/assets/cart-8d1e44a0.js:88:5)\n" +
		"    at https://shop.test/assets/vendor.js:1:400\n" +
		"    at Array.map (<anonymous>)")
	if typ != "TypeError" || !strings.Contains(val, "reading 'price'") {
		t.Fatalf("head = %q %q", typ, val)
	}
	if len(fr) != 4 || fr[len(fr)-1].Function != "lineTotal" || fr[len(fr)-1].Line != 12 || fr[len(fr)-1].Col != 20 {
		t.Fatalf("frames = %+v", fr)
	}
	if fr[0].InApp {
		t.Fatal("<anonymous> counted as the app's own")
	}
	// Firefox
	_, _, fr = ParseStack("lineTotal@https://shop.test/a.js:12:20\nCartRow@https://shop.test/b.js:88:5\n@https://shop.test/c.js:1:1")
	if len(fr) != 3 || fr[2].Function != "lineTotal" {
		t.Fatalf("gecko frames = %+v", fr)
	}
}

func TestParseStackPythonAndGo(t *testing.T) {
	typ, val, fr := ParseStack("Traceback (most recent call last):\n  File \"/srv/run.py\", line 31, in main\n    import_file(p)\n  File \"/srv/orders.py\", line 142, in to_order\n    email = row['customer_email']\nKeyError: 'customer_email'")
	if typ != "KeyError" || val != "'customer_email'" || len(fr) != 2 || fr[1].Function != "to_order" || fr[1].ContextLine == "" {
		t.Fatalf("python = %q %q %+v", typ, val, fr)
	}
	typ, val, fr = ParseStack("panic: runtime error: index out of range [3] with length 3\n\ngoroutine 418 [running]:\n" +
		"github.com/x/notes/internal/server.(*Server).handleBacklinks(0xc0001c2000, {0x1a2b3c0, 0xc0004d6000}, 0xc000512000)\n\t/src/internal/server/links.go:233 +0x5b4\n" +
		"net/http.HandlerFunc.ServeHTTP(...)\n\t/usr/local/go/src/net/http/server.go:2220\n")
	if typ != "panic" || !strings.HasPrefix(val, "runtime error: index out of range") {
		t.Fatalf("go head = %q %q", typ, val)
	}
	if len(fr) != 2 || fr[1].Function != "(*Server).handleBacklinks" || fr[1].Module != "github.com/x/notes/internal/server" || fr[1].Line != 233 || !fr[1].InApp || fr[0].InApp {
		t.Fatalf("go frames = %+v", fr)
	}
}

// The same error with other numbers and ids is one issue; another place in
// the code is another; a new build (other bundle hash, other line) keeps it.
func TestFingerprint(t *testing.T) {
	a := FromMap(map[string]any{"stack": "TypeError: x is undefined (id 41)\n    at f (https://s/a-1b2c3d4e.js:12:20)"})
	b := FromMap(map[string]any{"stack": "TypeError: x is undefined (id 977)\n    at f (https://s/a-9f8e7d6c.js:14:2)"})
	c := FromMap(map[string]any{"stack": "TypeError: x is undefined (id 41)\n    at g (https://s/a-1b2c3d4e.js:12:20)"})
	if Fingerprint(1, &a.Data) != Fingerprint(1, &b.Data) {
		t.Fatal("same problem, two fingerprints")
	}
	if Fingerprint(1, &a.Data) == Fingerprint(1, &c.Data) {
		t.Fatal("another function, same fingerprint")
	}
	m1 := FromMap(map[string]any{"message": "user 18 not found in 'orders' (took 12ms)"})
	m2 := FromMap(map[string]any{"message": "user 2001 not found in 'archive' (took 340ms)"})
	if Fingerprint(1, &m1.Data) != Fingerprint(1, &m2.Data) {
		t.Fatal("messages differing only in values got two fingerprints")
	}
	own := FromMap(map[string]any{"message": "x", "fingerprint": []any{"payments"}})
	own2 := FromMap(map[string]any{"message": "y", "fingerprint": []any{"payments"}})
	if Fingerprint(1, &own.Data) != Fingerprint(1, &own2.Data) {
		t.Fatal("the sender's own fingerprint was not used")
	}
}

func TestEnvelope(t *testing.T) {
	ev := `{"exception":{"values":[{"type":"Error","value":"boom"}]}}`
	env := "{\"event_id\":\"9ec79c33ec9942ab8353589fcb2e04dc\",\"dsn\":\"https://abc@h/3\"}\n" +
		"{\"type\":\"transaction\"}\n{\"spans\":[]}\n" +
		"{\"type\":\"event\"}\n" + ev + "\n"
	evs, dsn, err := ParseEnvelope([]byte(env))
	if err != nil || len(evs) != 1 || evs[0].EventID != "9ec79c33ec9942ab8353589fcb2e04dc" || KeyFromDSN(dsn) != "abc" {
		t.Fatalf("envelope: %v %v %q", err, evs, dsn)
	}
	if _, _, err := ParseEnvelope([]byte("{}\n{\"type\":\"session\"}\n{}\n")); err != ErrNoEvent {
		t.Fatalf("sessions only: %v", err)
	}
	if k := SentryKey("Sentry sentry_version=7, sentry_key=abc123, sentry_client=x"); k != "abc123" {
		t.Fatalf("key = %q", k)
	}
}

// Container logs: slog errors become events (warnings only when asked),
// info lines do not, a panic over many lines is one event.
func TestLogReader(t *testing.T) {
	r := &LogReader{Container: "pocket-notes"}
	at := time.Now()
	var got []*Event
	for _, l := range []string{
		`time=2026-11-07T09:12:44.120+01:00 level=INFO msg="http" method=GET path=/ status=200`,
		`time=2026-11-07T09:12:44.120+01:00 level=WARN msg="slow query" ms=900`,
		`time=2026-11-07T09:12:44.120+01:00 level=ERROR msg="save note" err="database is locked" id=812 method=PUT path=/api/v1/notes/812`,
		`{"level":"error","ts":1730970000,"logger":"http.log.error","msg":"dial tcp 172.18.0.5:8080: connect: connection refused"}`,
		`panic: assignment to entry in nil map`,
		``,
		`goroutine 7 [running]:`,
		`main.(*Server).save(...)`,
		"\t/src/main.go:42 +0x1d",
		`time=2026-11-07T09:13:00.000+01:00 level=INFO msg="back"`,
	} {
		got = append(got, r.Line(at, l)...)
	}
	if len(got) != 3 {
		for _, e := range got {
			t.Logf("%s %q %v", e.Level, e.Data.Message, e.Data.Exceptions)
		}
		t.Fatalf("got %d events, want 3", len(got))
	}
	if got[0].Data.Message != "save note: database is locked" || got[0].Data.Request == nil || got[0].Data.Request.Method != "PUT" {
		t.Fatalf("slog event = %+v", got[0].Data)
	}
	if got[1].Data.Logger != "http.log.error" {
		t.Fatalf("json logger = %q", got[1].Data.Logger)
	}
	p := got[2]
	if p.Level != "fatal" || len(p.Data.Exceptions) != 1 || p.Data.Exceptions[0].Type != "panic" || len(p.Data.Exceptions[0].Frames) != 1 {
		t.Fatalf("panic = %+v", p.Data.Exceptions)
	}
	w := &LogReader{Container: "x", Warnings: true}
	if evs := w.Line(at, `time=x level=WARN msg="slow query"`); len(evs) != 1 || evs[0].Level != "warning" {
		t.Fatalf("warnings asked for: %v", evs)
	}
}

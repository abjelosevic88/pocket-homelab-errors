package server

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func project(a *app, tok, name, platform string) map[string]any {
	a.t.Helper()
	return a.ok(tok, "POST", "/api/v1/projects", map[string]any{"name": name, "platform": platform})
}

func sentryEvent(typ, value string, frames ...string) []byte {
	fr := []any{}
	for i, f := range frames {
		fr = append(fr, map[string]any{"function": f, "filename": "/app/" + f + ".js", "lineno": 10 + i, "in_app": true})
	}
	b, _ := json.Marshal(map[string]any{
		"platform": "javascript", "level": "error", "release": "web@1.0", "environment": "production",
		"exception": map[string]any{"values": []any{map[string]any{"type": typ, "value": value, "stacktrace": map[string]any{"frames": fr}}}},
		"request":   map[string]any{"url": "https://shop.test/cart?token=s3cret&page=2", "headers": map[string]any{"Cookie": "sid=1", "Accept": "text/html"}},
		"user":      map[string]any{"ip_address": "10.1.2.3"},
		"extra":     map[string]any{"password": "hunter2", "row": 4},
	})
	return b
}

// Everything is per person: Bob never sees, changes or removes Alice's
// projects or issues — they are not there for him (404) — and her key does
// not send into his account.
func TestPerPersonIsolation(t *testing.T) {
	a := newApp(t, nil)
	p := project(a, a.alice, "Shop", "javascript")
	pid := id(p)
	ev := a.ok(a.alice, "POST", "/api/v1/projects/"+pid+"/events", map[string]any{"type": "TypeError", "value": "x is undefined", "stack": "TypeError: x is undefined\n    at f (https://shop.test/a.js:1:2)"})
	iid := itoa(int64(num(ev, "issue_id")))
	for _, path := range []string{"/api/v1/projects/" + pid, "/api/v1/issues/" + iid, "/api/v1/issues/" + iid + "/latest", "/api/v1/issues/" + iid + "/events", "/api/v1/issues/" + iid + "/markdown"} {
		a.expect(404, a.bob, "GET", path, nil)
	}
	a.expect(404, a.bob, "PUT", "/api/v1/projects/"+pid, map[string]any{"name": "mine now"})
	a.expect(404, a.bob, "DELETE", "/api/v1/projects/"+pid, nil)
	a.expect(404, a.bob, "POST", "/api/v1/projects/"+pid+"/events", map[string]any{"message": "hi"})
	a.expect(404, a.bob, "POST", "/api/v1/issues/"+iid+"/status", map[string]any{"status": "resolved"})
	a.expect(404, a.bob, "PUT", "/api/v1/issues/"+iid, map[string]any{"note": "mine"})
	a.expect(404, a.bob, "DELETE", "/api/v1/issues/"+iid, nil)
	if ch := a.ok(a.bob, "POST", "/api/v1/issues/status", map[string]any{"ids": []int64{n(iid)}, "status": "resolved"})["changed"].([]any); len(ch) != 0 {
		t.Fatalf("bob resolved alice's issue: %v", ch)
	}
	if l := a.ok(a.bob, "GET", "/api/v1/issues?status=all", nil)["issues"].([]any); len(l) != 0 {
		t.Fatalf("bob sees %d issues", len(l))
	}
	if l := a.ok(a.bob, "GET", "/api/v1/projects", nil)["projects"].([]any); len(l) != 0 {
		t.Fatalf("bob sees %d projects", len(l))
	}
	if r := a.ok(a.bob, "GET", "/api/v1/search?q=undefined", nil)["results"].([]any); len(r) != 0 {
		t.Fatalf("bob finds alice's issue: %v", r)
	}
	if o := a.ok(a.bob, "GET", "/api/v1/summary", nil)["overview"].(map[string]any); num(o, "open") != 0 {
		t.Fatalf("bob's summary counts alice's issues: %v", o)
	}
	// Alice still has hers
	if l := a.ok(a.alice, "GET", "/api/v1/issues", nil)["issues"].([]any); len(l) != 1 {
		t.Fatalf("alice has %d issues", len(l))
	}
}

// A Sentry SDK sends to /api/{project}/store/ and /envelope/ with the key;
// the same problem lands in one issue, a different one in another; secrets
// and addresses are not kept; a wrong key or another project's id is refused.
func TestSentryIngest(t *testing.T) {
	a := newApp(t, nil)
	p := project(a, a.alice, "Shop", "javascript")
	pid, key := id(p), p["public_key"].(string)
	auth := map[string]string{"X-Sentry-Auth": "Sentry sentry_version=7, sentry_client=sentry.javascript.browser/8.0.0, sentry_key=" + key}
	code, out := a.raw("POST", "/api/"+pid+"/store/", sentryEvent("TypeError", "a.b is undefined (id 41)", "renderCart", "lineTotal"), auth)
	if code != 200 || out["id"] == "" {
		t.Fatalf("store → %d %v", code, out)
	}
	// the same error with another number, gzipped, keyed in the query
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	_, _ = zw.Write(sentryEvent("TypeError", "a.b is undefined (id 977)", "renderCart", "lineTotal"))
	_ = zw.Close()
	code, out = a.raw("POST", "/api/"+pid+"/store/?sentry_key="+key, gz.Bytes(), map[string]string{"Content-Encoding": "gzip"})
	if code != 200 {
		t.Fatalf("gzip store → %d %v", code, out)
	}
	// an envelope with a session (skipped) and a different error
	evb := sentryEvent("RangeError", "Invalid time value", "formatDay")
	env := fmt.Sprintf("{\"event_id\":\"9ec79c33ec9942ab8353589fcb2e04dc\",\"dsn\":\"https://%s@errors.test/%s\"}\n{\"type\":\"session\"}\n{\"sid\":\"x\"}\n{\"type\":\"event\",\"length\":%d}\n%s\n", key, pid, len(evb), evb)
	code, out = a.raw("POST", "/api/"+pid+"/envelope/", []byte(env), map[string]string{"Content-Type": "text/plain;charset=UTF-8"})
	if code != 200 || out["id"] != "9ec79c33ec9942ab8353589fcb2e04dc" {
		t.Fatalf("envelope → %d %v", code, out)
	}
	// the same envelope again is not counted twice
	a.raw("POST", "/api/"+pid+"/envelope/", []byte(env), nil)
	issues := a.ok(a.alice, "GET", "/api/v1/issues", nil)["issues"].([]any)
	if len(issues) != 2 {
		t.Fatalf("want 2 issues, got %d", len(issues))
	}
	byType := map[string]map[string]any{}
	for _, x := range issues {
		is := x.(map[string]any)
		byType[is["type"].(string)] = is
	}
	if num(byType["TypeError"], "times") != 2 || num(byType["RangeError"], "times") != 1 {
		t.Fatalf("grouping: %v", byType)
	}
	if c := byType["TypeError"]["culprit"]; c != "lineTotal (lineTotal.js)" {
		t.Fatalf("culprit = %v", c)
	}
	ev := a.ok(a.alice, "GET", "/api/v1/issues/"+id(byType["TypeError"])+"/latest", nil)["event"].(map[string]any)
	raw, _ := json.Marshal(ev)
	for _, secret := range []string{"s3cret", "sid=1", "hunter2", "10.1.2.3"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("kept %q: %s", secret, raw)
		}
	}
	// wrong key, another project's id, no key
	other := project(a, a.bob, "Bob's", "go")
	if code, _ := a.raw("POST", "/api/"+id(other)+"/store/", sentryEvent("E", "x"), auth); code != 401 {
		t.Fatalf("alice's key into bob's project → %d", code)
	}
	if code, _ := a.raw("POST", "/api/"+pid+"/store/?sentry_key=nope", sentryEvent("E", "x"), nil); code != 401 {
		t.Fatalf("wrong key → %d", code)
	}
	if code, _ := a.raw("OPTIONS", "/api/"+pid+"/store/", nil, map[string]string{"Origin": "https://shop.test"}); code != 204 {
		t.Fatalf("preflight → %d", code)
	}
}

// A resolved issue that happens again is open and marked as back; an
// issue ignored for good stays ignored.
func TestRegressionAndIgnore(t *testing.T) {
	a := newApp(t, nil)
	pid := id(project(a, a.alice, "API", "go"))
	send := func(msg string) string {
		r := a.ok(a.alice, "POST", "/api/v1/projects/"+pid+"/events", map[string]any{"message": msg, "level": "error"})
		return itoa(int64(num(r, "issue_id")))
	}
	iid := send("payment provider timed out after 30s")
	a.ok(a.alice, "POST", "/api/v1/issues/"+iid+"/status", map[string]any{"status": "resolved"})
	if again := send("payment provider timed out after 31s"); again != iid {
		t.Fatalf("same problem became issue %s, not %s", again, iid)
	}
	is := a.ok(a.alice, "GET", "/api/v1/issues/"+iid, nil)
	if is["status"] != "open" || is["regressed"] != true {
		t.Fatalf("after coming back: %v %v", is["status"], is["regressed"])
	}
	other := send("disk full on /data")
	a.ok(a.alice, "POST", "/api/v1/issues/"+other+"/status", map[string]any{"status": "ignored"})
	send("disk full on /data")
	if st := a.ok(a.alice, "GET", "/api/v1/issues/"+other, nil)["status"]; st != "ignored" {
		t.Fatalf("ignored issue is %v", st)
	}
	a.expect(400, a.alice, "POST", "/api/v1/issues/"+other+"/status", map[string]any{"status": "ignored", "for": "3w"})
}

// An export imported again adds nothing; into an empty account it brings
// everything back.
func TestExportImportIdempotent(t *testing.T) {
	a := newApp(t, nil)
	pid := id(project(a, a.alice, "Shop", "javascript"))
	for i := 0; i < 3; i++ {
		a.ok(a.alice, "POST", "/api/v1/projects/"+pid+"/events", map[string]any{"type": "TypeError", "value": fmt.Sprintf("bad %d", i), "stack": "TypeError: bad\n    at f (https://x/a.js:1:2)"})
	}
	a.ok(a.alice, "POST", "/api/v1/projects/"+pid+"/events", map[string]any{"message": "something else"})
	req, _ := http.NewRequest("GET", a.url+"/api/v1/export.json", nil)
	req.Header.Set("Authorization", "Bearer "+a.alice)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	_ = json.NewDecoder(res.Body).Decode(&doc)
	res.Body.Close()
	again := a.ok(a.alice, "POST", "/api/v1/import", doc)
	added := again["added"].(map[string]any)
	for k, v := range added {
		if num(added, k) != 0 {
			t.Fatalf("re-import added %s: %v", k, v)
		}
	}
	into := a.ok(a.bob, "POST", "/api/v1/import", doc)["added"].(map[string]any)
	if num(into, "projects") != 1 || num(into, "issues") != 2 || num(into, "events") != 4 {
		t.Fatalf("into bob: %v", into)
	}
	bobs := a.ok(a.bob, "GET", "/api/v1/projects", nil)["projects"].([]any)
	if bobs[0].(map[string]any)["public_key"] == id(map[string]any{}) || bobs[0].(map[string]any)["public_key"] == a.ok(a.alice, "GET", "/api/v1/projects/"+pid, nil)["public_key"] {
		t.Fatal("bob's imported project shares alice's key")
	}
	a.ok(a.bob, "POST", "/api/v1/import", doc)
	if l := a.ok(a.bob, "GET", "/api/v1/issues?status=all", nil)["issues"].([]any); len(l) != 2 {
		t.Fatalf("bob has %d issues after a second import", len(l))
	}
}

// Settings are per person; only an admin may have the containers read.
func TestSettingsPerPerson(t *testing.T) {
	a := newApp(t, nil)
	a.ok(a.alice, "PUT", "/api/v1/settings", map[string]any{"keep_days": 7, "notify_via": "off"})
	if st := a.ok(a.bob, "GET", "/api/v1/settings", nil); num(st, "keep_days") != 30 || st["notify_via"] != "auto" {
		t.Fatalf("bob got alice's settings: %v %v", st["keep_days"], st["notify_via"])
	}
	a.expect(403, a.bob, "PUT", "/api/v1/settings", map[string]any{"watch_docker": true})
	a.ok(a.alice, "PUT", "/api/v1/settings", map[string]any{"watch_docker": true, "watch_pattern": "pocket-*, caddy"})
	a.expect(400, a.alice, "PUT", "/api/v1/settings", map[string]any{"keep_days": 0})
}

// A Pocket Core link reaches only what its scopes cover.
func TestLinkScopes(t *testing.T) {
	cases := []struct {
		method, path string
		scopes       []string
		want         bool
	}{
		{"GET", "/api/v1/auth/me", nil, true},
		{"GET", "/api/v1/issues", []string{"errors:read"}, true},
		{"GET", "/api/v1/issues/4/latest", []string{"errors:read"}, true},
		{"GET", "/api/v1/day/2026-11-07", []string{"errors:read"}, true},
		{"GET", "/api/v1/logs", []string{"errors:read"}, true},
		{"GET", "/api/v1/logs/overview", []string{"errors:read"}, true},
		{"GET", "/api/v1/logs/lines/9/context", []string{"errors:read"}, true},
		{"GET", "/api/v1/issues/4/logs", []string{"errors:read"}, true},
		{"PUT", "/api/v1/logs/settings", []string{"errors:read", "errors:write"}, false},
		{"POST", "/api/v1/logs/clear", []string{"errors:read", "errors:write"}, false},
		{"GET", "/api/v1/logs/export.txt", []string{"errors:read"}, false},
		{"GET", "/api/v1/issues", []string{"errors:write"}, false},
		{"POST", "/api/v1/report", []string{"errors:write"}, true},
		{"POST", "/api/v1/report", []string{"errors:read"}, false},
		{"POST", "/api/v1/issues/4/status", []string{"errors:write"}, true},
		{"DELETE", "/api/v1/issues/4", []string{"errors:write"}, false},
		{"POST", "/api/v1/projects", []string{"errors:write"}, false},
		{"GET", "/api/v1/settings", []string{"errors:read", "errors:write"}, false},
		{"GET", "/api/v1/export.json", []string{"errors:read"}, false},
	}
	for _, c := range cases {
		r := httptest.NewRequest(c.method, c.path, nil)
		if got := linkAllows(c.scopes, r); got != c.want {
			t.Errorf("%s %s with %v = %v, want %v", c.method, c.path, c.scopes, got, c.want)
		}
	}
}

// The demo has projects, open and resolved issues, one back after it was
// resolved, and an explanation.
func TestDemo(t *testing.T) {
	a := newApp(t, nil)
	if err := a.s.seedDemo(t.Context(), a.ids["Alice"]); err != nil {
		t.Fatal(err)
	}
	sum := a.ok(a.alice, "GET", "/api/v1/summary", nil)["overview"].(map[string]any)
	if num(sum, "open") < 6 || num(sum, "regressed") != 1 || num(sum, "events_24h") == 0 {
		t.Fatalf("demo summary: %v", sum)
	}
	if l := a.ok(a.alice, "GET", "/api/v1/issues?status=resolved", nil)["issues"].([]any); len(l) < 3 {
		t.Fatalf("demo has %d resolved issues", len(l))
	}
	if r := a.ok(a.alice, "GET", "/api/v1/search?q=price", nil)["results"].([]any); len(r) == 0 {
		t.Fatal("search finds nothing for price")
	}
}

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	core "github.com/abjelosevic88/pocket-homelab-core/sdk"
	"github.com/abjelosevic88/pocket-homelab-errors/internal/config"
	"github.com/abjelosevic88/pocket-homelab-errors/internal/store"
)

// fakeCapCore is a Pocket Core that vouches for link tokens
// pcx_<alice|bob>_<search|day|widget|capture|read|write> from Pocket Hub
// and remembers the manifest Errors registers with.
type fakeCapCore struct {
	*httptest.Server
	mu       sync.Mutex
	manifest map[string]any
}

func newCapApp(t *testing.T) (*app, *fakeCapCore) {
	t.Helper()
	fc := &fakeCapCore{}
	scopes := map[string]string{"search": core.ScopeSearch, "day": core.ScopeDay, "widget": core.ScopeWidget, "capture": core.ScopeCapture,
		"read": "errors:read", "write": "errors:write"}
	fc.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/apps/register":
			var m map[string]any
			_ = json.NewDecoder(r.Body).Decode(&m)
			fc.mu.Lock()
			fc.manifest = m
			fc.mu.Unlock()
			io.WriteString(w, `{}`)
		case "/api/v1/auth/introspect":
			var in struct{ Token string }
			_ = json.NewDecoder(r.Body).Decode(&in)
			parts := strings.Split(in.Token, "_")
			if len(parts) != 3 || parts[0] != "pcx" || scopes[parts[2]] == "" {
				io.WriteString(w, `{"active":false}`)
				return
			}
			email := parts[1] + "@example.com"
			fmt.Fprintf(w, `{"active":true,"kind":"delegated","scope":%q,"audience":"errors","user":{"id":1,"sub":%q,"email":%q,"name":%q,"role":"member"},"app":{"id":9,"slug":"hub","name":"Pocket Hub"}}`,
				scopes[parts[2]], email, email, parts[1])
		default:
			io.WriteString(w, `{}`)
		}
	}))
	t.Cleanup(fc.Close)
	return newApp(t, func(c *config.Config) { c.CoreURL, c.CoreRegistrationKey = fc.URL, "key" }), fc
}

// record files one error at a time, as ingest would.
func (a *app) record(who, pid, fp, title string, at time.Time) int64 {
	a.t.Helper()
	ctx := context.Background()
	p, err := a.st.GetProject(ctx, a.ids[who], n(pid))
	if err != nil {
		a.t.Fatal(err)
	}
	res, err := a.st.Record(ctx, p, store.Incoming{At: at, Fingerprint: fp, Title: title, Culprit: "app/main.js in addItem", Type: "TypeError", Level: "error",
		Data: store.EventData{}})
	if err != nil {
		a.t.Fatal(err)
	}
	return res.Issue.ID
}

// items are the rows of a capability answer.
func items(t *testing.T, m map[string]any) []map[string]any {
	t.Helper()
	l, ok := m["items"].([]any)
	if !ok {
		t.Fatalf("no items list in %v", m)
	}
	out := []map[string]any{}
	for _, v := range l {
		out = append(out, v.(map[string]any))
	}
	return out
}

// noCode fails when an answer carries what only the app's own pages show:
// the culprit, a stack or an event's text.
func noCode(t *testing.T, m map[string]any) {
	t.Helper()
	b, _ := json.Marshal(m)
	for _, bad := range []string{"main.js", "addItem", "stack", "frames", "culprit", "fingerprint"} {
		if strings.Contains(string(b), bad) {
			t.Fatalf("answer holds %q: %s", bad, b)
		}
	}
}

func TestPocketSearch(t *testing.T) {
	a := newApp(t, nil)
	shop := id(project(a, a.alice, "Shop", "javascript"))
	blog := id(project(a, a.alice, "Blog", "javascript"))
	now := time.Now()
	gone := a.record("Alice", shop, "a", "TypeError: cart is empty", now.Add(-time.Second))
	a.ok(a.alice, "POST", "/api/v1/issues/"+itoa(gone)+"/status", map[string]any{"status": "resolved"})
	cart := a.record("Alice", shop, "b", "TypeError: cart is undefined", now.Add(-3*time.Second))
	a.record("Alice", shop, "b", "TypeError: cart is undefined", now.Add(-2*time.Second))
	a.record("Alice", blog, "c", "Comments failed to load", now)
	bobs := id(project(a, a.bob, "Bob's shop", "javascript"))
	a.record("Bob", bobs, "d", "cart broke", now)

	res := a.ok(a.alice, "GET", "/api/v1/pocket/search?q=cart", nil)
	noCode(t, res)
	got := items(t, res)
	// open before resolved, though the resolved one was seen later
	if len(got) != 2 || got[0]["id"] != itoa(cart) || got[1]["id"] != itoa(gone) {
		t.Fatalf("cart: %v", got)
	}
	if it := got[0]; it["title"] != "TypeError: cart is undefined" || it["kind"] != "issue" || it["url"] != "/issues/"+itoa(cart) ||
		it["snippet"] != "Shop · open · 2 times · last seen today" || it["date"] != store.Stamp(now.Add(-2*time.Second)) {
		t.Fatalf("the open one: %v", it)
	}
	if got[1]["snippet"] != "Shop · resolved · 1 time · last seen today" {
		t.Fatalf("the resolved one: %v", got[1])
	}
	// by project, and by where it broke
	if got := items(t, a.ok(a.alice, "GET", "/api/v1/pocket/search?q=blog", nil)); len(got) != 1 || got[0]["title"] != "Comments failed to load" {
		t.Fatalf("by project: %v", got)
	}
	if got := items(t, a.ok(a.alice, "GET", "/api/v1/pocket/search?q=addItem", nil)); len(got) != 3 {
		t.Fatalf("by culprit: %v", got)
	}
	if got := items(t, a.ok(a.alice, "GET", "/api/v1/pocket/search?q=cart&limit=1", nil)); len(got) != 1 || got[0]["id"] != itoa(cart) {
		t.Fatalf("limit 1: %v", got)
	}
	for _, q := range []string{"", "%3F%3F", "*", "%25"} {
		if got := items(t, a.ok(a.alice, "GET", "/api/v1/pocket/search?q="+q, nil)); len(got) != 0 {
			t.Fatalf("q=%s finds %v", q, got)
		}
	}
	// another person's issues never appear
	if got := items(t, a.ok(a.bob, "GET", "/api/v1/pocket/search?q=cart", nil)); len(got) != 1 || got[0]["title"] != "cart broke" {
		t.Fatalf("bob: %v", got)
	}
	a.expect(401, "", "GET", "/api/v1/pocket/search?q=cart", nil)
}

func TestPocketDay(t *testing.T) {
	a := newApp(t, nil)
	shop := id(project(a, a.alice, "Shop", "javascript"))
	now := time.Now()
	then := now.Add(-72 * time.Hour)
	today, past := now.Format("2006-01-02"), then.Format("2006-01-02")

	// back: first seen three days ago, resolved, happened again today
	back := a.record("Alice", shop, "old", "Payment timed out", then)
	a.ok(a.alice, "POST", "/api/v1/issues/"+itoa(back)+"/status", map[string]any{"status": "resolved"})
	a.record("Alice", shop, "old", "Payment timed out", now)
	// new today, twice
	fresh := a.record("Alice", shop, "new", "TypeError: cart is undefined", now)
	a.record("Alice", shop, "new", "TypeError: cart is undefined", now)
	// seen before and again today, never resolved: neither new nor back
	a.record("Alice", shop, "seen", "Slow page", then)
	a.record("Alice", shop, "seen", "Slow page", now)

	res := a.ok(a.alice, "GET", "/api/v1/pocket/day/"+today, nil)
	noCode(t, res)
	if res["date"] != today || res["summary"] != "1 new issue, 1 came back" {
		t.Fatalf("today: %v", res)
	}
	got := items(t, res)
	if len(got) != 2 {
		t.Fatalf("today's items: %v", got)
	}
	byID := map[string]map[string]any{}
	for _, it := range got {
		byID[it["id"].(string)] = it
	}
	if it := byID[itoa(fresh)]; it == nil || it["kind"] != "new" || it["title"] != "TypeError: cart is undefined" || it["sub"] != "Shop · 2 times" ||
		it["url"] != "/issues/"+itoa(fresh) || it["time"] != now.Format("15:04") {
		t.Fatalf("new: %v", it)
	}
	if it := byID[itoa(back)]; it == nil || it["kind"] != "came back" || it["sub"] != "Shop · 1 time" || it["time"] != now.Format("15:04") {
		t.Fatalf("came back: %v", it)
	}
	// every error of the day counts, the slow page's too
	if metrics := fmt.Sprint(res["metrics"]); metrics != "[map[label:New value:1] map[label:Came back value:1] map[label:Errors value:4]]" {
		t.Fatalf("metrics: %s", metrics)
	}
	// three days ago both started
	if res := a.ok(a.alice, "GET", "/api/v1/pocket/day/"+past+"?tz=Europe/Sarajevo", nil); res["summary"] != "2 new issues" || len(items(t, res)) != 2 {
		t.Fatalf("three days ago: %v", res)
	}
	// a quiet day says nothing
	if res := a.ok(a.alice, "GET", "/api/v1/pocket/day/2020-01-01", nil); res["summary"] != nil || len(items(t, res)) != 0 {
		t.Fatalf("quiet day: %v", res)
	}
	a.expect(400, a.alice, "GET", "/api/v1/pocket/day/2020-13-01", nil)
	// another person's day
	if res := a.ok(a.bob, "GET", "/api/v1/pocket/day/"+today, nil); res["summary"] != nil || len(items(t, res)) != 0 {
		t.Fatalf("bob's day: %v", res)
	}
}

func TestPocketWidget(t *testing.T) {
	a := newApp(t, nil)
	if w := a.ok(a.alice, "GET", "/api/v1/pocket/widget", nil); len(items(t, w)) != 0 || w["line"] != nil {
		t.Fatalf("nothing yet: %v", w)
	}
	shop := id(project(a, a.alice, "Shop", "javascript"))
	now := time.Now()
	then := now.Add(-72 * time.Hour)
	busy := a.record("Alice", shop, "busy", "Slow page", then)
	for range 5 {
		a.record("Alice", shop, "busy", "Slow page", now)
	}
	fresh := a.record("Alice", shop, "new", "TypeError: cart is undefined", now)
	a.record("Alice", shop, "new", "TypeError: cart is undefined", now)
	quiet := a.record("Alice", shop, "quiet", "Old trouble", then)
	done := a.record("Alice", shop, "done", "Fixed already", then)
	a.ok(a.alice, "POST", "/api/v1/issues/"+itoa(done)+"/status", map[string]any{"status": "resolved"})

	w := a.ok(a.alice, "GET", "/api/v1/pocket/widget", nil)
	noCode(t, w)
	got := items(t, w)
	if len(got) != 2 || got[0]["id"] != itoa(fresh) || got[1]["id"] != itoa(busy) {
		t.Fatalf("new first, then the busiest; quiet (%d) and resolved (%d) left out: %v", quiet, done, got)
	}
	if got[0]["detail"] != "Shop · 2 times today" || got[1]["detail"] != "Shop · 5 times today" || got[0]["url"] != "/issues/"+itoa(fresh) || got[0]["title"] != "TypeError: cart is undefined" {
		t.Fatalf("rows: %v", got)
	}
	if w["line"] != "1 new issue today" || w["url"] != "/" || fmt.Sprint(w["metrics"]) != "[map[label:Open value:3] map[label:New today value:1]]" {
		t.Fatalf("widget: %v", w)
	}
	// the resolved one happens again: it came back
	a.record("Alice", shop, "done", "Fixed already", now)
	if w := a.ok(a.alice, "GET", "/api/v1/pocket/widget", nil); w["line"] != "1 new issue today, 1 came back" {
		t.Fatalf("came back: %v", w)
	}
	// another person's widget
	if w := a.ok(a.bob, "GET", "/api/v1/pocket/widget", nil); len(items(t, w)) != 0 || w["line"] != nil || w["metrics"] != nil {
		t.Fatalf("bob's widget: %v", w)
	}
}

// Through a Pocket Core link each pocket:* scope opens its one route (and
// /auth/me), nothing else: not the issue list, not an issue's events, not the
// logs, not resolving. errors:read and errors:write do not open them.
func TestPocketLinks(t *testing.T) {
	a, _ := newCapApp(t)
	shop := id(project(a, a.alice, "Shop", "javascript"))
	iss := itoa(a.record("Alice", shop, "x", "TypeError: cart is undefined", time.Now()))
	today := time.Now().Format("2006-01-02")
	for _, c := range []struct {
		tok, method, path string
		want              int
	}{
		{"pcx_alice_search", "GET", "/api/v1/pocket/search?q=cart", 200},
		{"pcx_alice_day", "GET", "/api/v1/pocket/day/" + today, 200},
		{"pcx_alice_widget", "GET", "/api/v1/pocket/widget", 200},
		{"pcx_alice_widget", "GET", "/api/v1/auth/me", 200},
		// another capability's route
		{"pcx_alice_search", "GET", "/api/v1/pocket/day/" + today, 403},
		{"pcx_alice_search", "GET", "/api/v1/pocket/widget", 403},
		{"pcx_alice_day", "GET", "/api/v1/pocket/search?q=cart", 403},
		{"pcx_alice_widget", "GET", "/api/v1/pocket/day/" + today, 403},
		// no capture here
		{"pcx_alice_capture", "POST", "/api/v1/pocket/capture", 404},
		{"pcx_alice_capture", "GET", "/api/v1/pocket/widget", 403},
		// the rest of the app
		{"pcx_alice_search", "GET", "/api/v1/search?q=cart", 403},
		{"pcx_alice_search", "GET", "/api/v1/issues", 403},
		{"pcx_alice_day", "GET", "/api/v1/day/" + today, 403},
		{"pcx_alice_widget", "GET", "/api/v1/summary", 403},
		{"pcx_alice_widget", "GET", "/api/v1/issues/" + iss, 403},
		{"pcx_alice_widget", "GET", "/api/v1/issues/" + iss + "/latest", 403},
		{"pcx_alice_search", "GET", "/api/v1/issues/" + iss + "/events", 403},
		{"pcx_alice_widget", "GET", "/api/v1/logs", 403},
		{"pcx_alice_search", "GET", "/api/v1/logs/patterns", 403},
		{"pcx_alice_day", "GET", "/api/v1/issues/" + iss + "/logs", 403},
		{"pcx_alice_widget", "POST", "/api/v1/issues/" + iss + "/status", 403},
		{"pcx_alice_search", "POST", "/api/v1/issues/status", 403},
		{"pcx_alice_day", "POST", "/api/v1/report", 403},
		// the app's own scopes do not open the new routes
		{"pcx_alice_read", "GET", "/api/v1/pocket/search?q=cart", 403},
		{"pcx_alice_read", "GET", "/api/v1/pocket/day/" + today, 403},
		{"pcx_alice_read", "GET", "/api/v1/pocket/widget", 403},
		{"pcx_alice_write", "GET", "/api/v1/pocket/widget", 403},
		{"pcx_alice_read", "GET", "/api/v1/search?q=cart", 200},
		{"pcx_nobody", "GET", "/api/v1/pocket/widget", 401},
	} {
		if code, m, _ := a.call(c.tok, c.method, c.path, map[string]any{}); code != c.want {
			t.Errorf("%s %s %s → %d, want %d (%v)", c.tok, c.method, c.path, code, c.want, m)
		}
	}
	// the token acts for its person only
	if got := items(t, a.ok("pcx_alice_search", "GET", "/api/v1/pocket/search?q=cart", nil)); len(got) != 1 || got[0]["id"] != iss {
		t.Fatalf("alice through the link: %v", got)
	}
	if got := items(t, a.ok("pcx_bob_search", "GET", "/api/v1/pocket/search?q=cart", nil)); len(got) != 0 {
		t.Fatalf("bob through the link finds alice's issue: %v", got)
	}
	if w := a.ok("pcx_bob_widget", "GET", "/api/v1/pocket/widget", nil); len(items(t, w)) != 0 {
		t.Fatalf("bob's widget through the link: %v", w)
	}
	if d := a.ok("pcx_bob_day", "GET", "/api/v1/pocket/day/"+today, nil); len(items(t, d)) != 0 {
		t.Fatalf("bob's day through the link: %v", d)
	}
	// reading changed nothing
	if is := a.ok(a.alice, "GET", "/api/v1/issues/"+iss, nil); is["status"] != "open" {
		t.Fatalf("issue: %v", is)
	}
}

func TestPocketLinkScopes(t *testing.T) {
	req := func(m, p string) *http.Request { return httptest.NewRequest(m, p, nil) }
	for _, c := range []struct {
		scope string
		r     *http.Request
		want  bool
	}{
		{core.ScopeSearch, req("GET", "/api/v1/pocket/search?q=x"), true},
		{core.ScopeDay, req("GET", "/api/v1/pocket/day/2026-01-02"), true},
		{core.ScopeWidget, req("GET", "/api/v1/pocket/widget"), true},
		{core.ScopeWidget, req("GET", "/api/v1/auth/me"), true},
		{core.ScopeCapture, req("POST", "/api/v1/pocket/capture"), false},
		{core.ScopeSearch, req("POST", "/api/v1/pocket/search"), false},
		{core.ScopeDay, req("GET", "/api/v1/pocket/day/today"), false},
		{core.ScopeDay, req("GET", "/api/v1/day/2026-01-02"), false},
		{core.ScopeWidget, req("GET", "/api/v1/logs"), false},
		{"errors:read", req("GET", "/api/v1/pocket/widget"), false},
		{"errors:write", req("GET", "/api/v1/pocket/search"), false},
	} {
		if got := linkAllows([]string{c.scope}, c.r); got != c.want {
			t.Errorf("%s %s %s = %v, want %v", c.scope, c.r.Method, c.r.URL.Path, got, c.want)
		}
	}
}

// Errors tells Core what it can do, so Search, Day and Hub find it on their own.
func TestManifestCapabilities(t *testing.T) {
	a, fc := newCapApp(t)
	if err := a.s.registerWithCore(context.Background()); err != nil {
		t.Fatal(err)
	}
	fc.mu.Lock()
	m := fc.manifest
	fc.mu.Unlock()
	if m["slug"] != "errors" || m["category"] != "System" {
		t.Fatalf("manifest: %v %v", m["slug"], m["category"])
	}
	search, _ := m["search"].(map[string]any)
	day, _ := m["day"].(map[string]any)
	widget, _ := m["widget"].(map[string]any)
	if search == nil || fmt.Sprint(search["bangs"]) != "[errors error issue]" || fmt.Sprint(search["kinds"]) != "[issue]" ||
		day == nil || day["label"] != "Errors" || day["one"] != "issue" || day["many"] != "issues" || widget == nil || widget["title"] != "Errors" || m["capture"] != nil {
		t.Fatalf("capabilities: %v %v %v %v", search, day, widget, m["capture"])
	}
	offered := map[string]bool{}
	for _, o := range m["offers"].([]any) {
		offered[o.(map[string]any)["scope"].(string)] = true
	}
	for _, sc := range []string{"errors:read", "errors:write", core.ScopeSearch, core.ScopeDay, core.ScopeWidget} {
		if !offered[sc] {
			t.Errorf("%s is not offered", sc)
		}
	}
	if offered[core.ScopeCapture] {
		t.Error("pocket:capture is offered")
	}
}

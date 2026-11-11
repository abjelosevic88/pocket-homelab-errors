package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// A phone with the person's token names its own project (?source=), made
// on its first error and found again after; project_id still works; a bad
// slug is refused.
func TestReportBySource(t *testing.T) {
	a := newApp(t, nil)
	ev := map[string]any{"level": "error", "release": "1.0.0 (1411)", "environment": "personal",
		"exception": map[string]any{"values": []any{map[string]any{"type": "URLError", "value": "timed out",
			"stacktrace": map[string]any{"frames": []any{map[string]any{"function": "refresh()", "filename": "Dashboard.swift", "lineno": 12, "in_app": true}}}}}}}
	path := "/api/v1/report?source=pocket-homelab-ios&name=Pocket%20Homelab%20(iPhone)&platform=cocoa"
	first := a.expect(201, a.alice, "POST", path, ev)
	pid := itoa(int64(num(first, "project_id")))
	if first["outcome"] == nil || first["issue_id"] == nil || first["url"] == nil {
		t.Fatalf("answer: %v", first)
	}
	p := a.ok(a.alice, "GET", "/api/v1/projects/"+pid, nil)
	if p["name"] != "Pocket Homelab (iPhone)" || p["platform"] != "cocoa" || p["source"] != "pocket-homelab-ios" || p["notify_new"] != true {
		t.Fatalf("project: %v", p)
	}
	again := a.expect(201, a.alice, "POST", "/api/v1/report?source=pocket-homelab-ios", map[string]any{"message": "sync stalled", "level": "warning"})
	if itoa(int64(num(again, "project_id"))) != pid {
		t.Fatalf("second error went to project %v, not %s", again["project_id"], pid)
	}
	// without name and platform: the slug and cocoa
	other := a.expect(201, a.alice, "POST", "/api/v1/report?source=watch-app", map[string]any{"message": "x"})
	if op := a.ok(a.alice, "GET", "/api/v1/projects/"+itoa(int64(num(other, "project_id"))), nil); op["name"] != "watch-app" || op["platform"] != "cocoa" {
		t.Fatalf("defaults: %v", op)
	}
	// bob's source of the same name is his own
	if b := a.expect(201, a.bob, "POST", "/api/v1/report?source=pocket-homelab-ios", map[string]any{"message": "x"}); itoa(int64(num(b, "project_id"))) == pid {
		t.Fatal("bob's report went into alice's project")
	}
	// project_id wins over source, as before
	shop := id(project(a, a.alice, "Shop", "javascript"))
	if r := a.expect(201, a.alice, "POST", "/api/v1/report?project_id="+shop+"&source=pocket-homelab-ios", map[string]any{"message": "y"}); itoa(int64(num(r, "project_id"))) != shop {
		t.Fatalf("project_id ignored: %v", r)
	}
	for _, bad := range []string{"Pocket", "a", "-x", "docker", "has_underscore", strings.Repeat("x", 49)} {
		a.expect(400, a.alice, "POST", "/api/v1/report?source="+bad, map[string]any{"message": "x"})
	}
	a.expect(400, a.alice, "POST", "/api/v1/report", map[string]any{"message": "x"})
	a.expect(401, "", "POST", path, ev)
}

// A phone's lines are kept as a device source: levels, fields with secrets
// hidden, a clock ahead pulled back, lines older than the logs are kept
// dropped, a batch sent again not doubled; only admins, only when Logs is
// on; limits answered with 413. An issue from that phone shows its lines.
func TestLogsPush(t *testing.T) {
	a := newApp(t, nil)
	now := time.Now().UTC()
	stamp := func(d time.Duration) string { return now.Add(d).Format(time.RFC3339Nano) }
	body := map[string]any{"source": "pocket-homelab-ios", "name": "Pocket Homelab (iPhone)", "lines": []any{
		map[string]any{"at": stamp(-90 * time.Second), "level": "info", "message": "opened Notes", "fields": map[string]any{"screen": "notes", "count": 3}},
		map[string]any{"at": stamp(-60 * time.Second), "level": "warn", "message": "slow answer from notes.example.com", "fields": map[string]any{"ms": 2400, "token": "abc123secret"}},
		map[string]any{"at": stamp(-30 * time.Second), "level": "error", "message": "refresh failed: authorization=Bearer abcdefghijklmnop12345"},
		map[string]any{"at": stamp(30 * time.Minute), "level": "debug", "message": "clock ahead"},
		map[string]any{"at": stamp(-30 * 24 * time.Hour), "level": "info", "message": "too old"},
		map[string]any{"at": stamp(-time.Second), "level": "info", "message": "  "},
	}}
	a.expect(409, a.alice, "POST", "/api/v1/logs/push", body)
	a.ok(a.alice, "PUT", "/api/v1/logs/settings", map[string]any{"on": true})
	a.expect(401, "", "POST", "/api/v1/logs/push", body)
	a.expect(403, a.bob, "POST", "/api/v1/logs/push", body)
	out := a.ok(a.alice, "POST", "/api/v1/logs/push", body)
	if num(out, "accepted") != 4 || num(out, "dropped") != 2 || num(out, "source_id") == 0 {
		t.Fatalf("push: %v", out)
	}
	// the same batch again adds nothing (but the line from the future: it
	// is "now" again, a new time)
	again := a.ok(a.alice, "POST", "/api/v1/logs/push", body)
	if num(again, "duplicates") < 3 || num(again, "accepted")+num(again, "duplicates") != 4 {
		t.Fatalf("push again: %v", again)
	}
	ls := a.ok(a.alice, "GET", "/api/v1/logs?group=device", nil)["lines"].([]any)
	if len(ls) != 4+int(num(again, "accepted")) {
		t.Fatalf("device lines: %d", len(ls))
	}
	raw, _ := json.Marshal(ls)
	for _, secret := range []string{"abc123secret", "abcdefghijklmnop12345", "too old"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("kept %q: %s", secret, raw)
		}
	}
	levels := map[string]string{}
	for _, x := range ls {
		l := x.(map[string]any)
		levels[l["msg"].(string)] = l["level"].(string)
		if l["group"] != "device" || l["source_label"] != "Pocket Homelab (iPhone)" {
			t.Fatalf("line: %v", l)
		}
		if l["msg"] == "clock ahead" && num(l, "at") > float64(time.Now().Add(time.Minute).UnixMilli()) {
			t.Fatalf("a line from the future kept as %v", l["at"])
		}
		if l["msg"] == "opened Notes" {
			if at := l["attrs"].(map[string]any); at["screen"] != "notes" || at["count"] != "3" {
				t.Fatalf("fields: %v", at)
			}
		}
	}
	if levels["opened Notes"] != "info" || levels["slow answer from notes.example.com"] != "warning" || levels["clock ahead"] != "debug" || levels["refresh failed: authorization=••••"] != "error" {
		t.Fatalf("levels: %v", levels)
	}
	srcs := a.ok(a.alice, "GET", "/api/v1/logs/sources", nil)["sources"].([]any)
	found := false
	for _, x := range srcs {
		s := x.(map[string]any)
		if s["kind"] == "device" {
			found = s["name"] == "pocket-homelab-ios" && s["group"] == "device" && s["label"] == "Pocket Homelab (iPhone)"
		}
	}
	if !found {
		t.Fatalf("no device source: %v", srcs)
	}
	// an error from that phone shows what it wrote around it
	ev := a.expect(201, a.alice, "POST", "/api/v1/report?source=pocket-homelab-ios&name=Pocket%20Homelab%20(iPhone)", map[string]any{
		"message": "refresh failed", "level": "error", "timestamp": stamp(-30 * time.Second)})
	around := a.ok(a.alice, "GET", "/api/v1/issues/"+itoa(int64(num(ev, "issue_id")))+"/logs", nil)
	if around["source"] == nil || len(around["lines"].([]any)) < 3 {
		t.Fatalf("issue logs: %v", around)
	}
	// limits and the slug
	a.expect(400, a.alice, "POST", "/api/v1/logs/push", map[string]any{"source": "Not A Slug", "lines": []any{}})
	many := make([]any, 1001)
	for i := range many {
		many[i] = map[string]any{"at": stamp(0), "level": "info", "message": fmt.Sprintf("line %d", i)}
	}
	a.expect(413, a.alice, "POST", "/api/v1/logs/push", map[string]any{"source": "pocket-homelab-ios", "lines": many})
	big, _ := json.Marshal(map[string]any{"source": "pocket-homelab-ios", "lines": []any{map[string]any{"at": stamp(0), "message": strings.Repeat("x", 1<<20)}}})
	if code, _ := a.raw("POST", "/api/v1/logs/push", big, map[string]string{"Authorization": "Bearer " + a.alice, "Content-Type": "application/json"}); code != http.StatusRequestEntityTooLarge {
		t.Fatalf("1 MB body → %d", code)
	}
}

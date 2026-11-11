package server

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// What pocket-logs.sh sends: text lines with ?source= and the label in a
// header; an iPhone's syslog keeps its own time and process. The script
// itself is served. A Sentry SDK's log items land as the project's source.
func TestLogsPushText(t *testing.T) {
	a := newApp(t, nil)
	a.ok(a.alice, "PUT", "/api/v1/logs/settings", map[string]any{"on": true})
	at := time.Now().Add(-2 * time.Minute)
	stamp := at.Format("Jan _2 15:04:05")
	body := stamp + " Aleksandars-iPhone SpringBoard(FrontBoard)[57] <Notice>: Scene lifecycle\n" +
		stamp + " Aleksandars-iPhone kernel[0] <Error>: AppleH13CamIn failed\n" +
		"plain line, level=warn msg=\"low disk\"\n"
	hdr := map[string]string{"Authorization": "Bearer " + a.alice, "Content-Type": "text/plain; charset=utf-8", "X-Source-Label": "iPhone"}
	code, out := a.raw("POST", "/api/v1/logs/push?source=iphone", []byte(body), hdr)
	if code != 200 || num(out, "accepted") != 3 {
		t.Fatalf("text push → %d %v", code, out)
	}
	// sent again (the script retries): nothing doubled
	if _, again := a.raw("POST", "/api/v1/logs/push?source=iphone", []byte(body), hdr); num(again, "duplicates") != 2 {
		t.Fatalf("again: %v", again)
	}
	ls := a.ok(a.alice, "GET", "/api/v1/logs?source=iphone", nil)["lines"].([]any)
	var springboard map[string]any
	levels := map[string]string{}
	for _, x := range ls {
		l := x.(map[string]any)
		levels[l["msg"].(string)] = l["level"].(string)
		if strings.HasPrefix(l["msg"].(string), "SpringBoard:") {
			springboard = l
		}
		if l["source_label"] != "iPhone" || l["group"] != "device" {
			t.Fatalf("line: %v", l)
		}
	}
	if springboard == nil || springboard["attrs"].(map[string]any)["library"] != "FrontBoard" || int64(num(springboard, "at"))/1000 != at.Unix() {
		t.Fatalf("syslog line: %v", springboard)
	}
	if levels["kernel: AppleH13CamIn failed"] != "error" {
		t.Fatalf("levels: %v", levels)
	}
	a.expect(400, a.alice, "POST", "/api/v1/logs/push?source=No%20Good", nil)

	res, err := http.Get(a.url + "/pocket-logs.sh")
	if err != nil {
		t.Fatal(err)
	}
	script, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || !strings.HasPrefix(string(script), "#!/usr/bin/env bash") || !strings.Contains(string(script), "/api/v1/logs/push?source=") {
		t.Fatalf("script → %d %.60s", res.StatusCode, script)
	}

	// an iOS app with a Sentry SDK and its logs on
	p := project(a, a.alice, "Pocket AI iOS", "other")
	pid, key := id(p), p["public_key"].(string)
	logs := fmt.Sprintf(`{"items":[{"timestamp":%d.5,"level":"warn","body":"low memory","attributes":{"device.model":{"value":"iPhone17,1","type":"string"}}}]}`, time.Now().Unix())
	env := fmt.Sprintf("{\"dsn\":\"https://%s@errors.test/%s\"}\n{\"type\":\"log\",\"length\":%d}\n%s\n", key, pid, len(logs), logs)
	if code, out := a.raw("POST", "/api/"+pid+"/envelope/", []byte(env), nil); code != 200 {
		t.Fatalf("log envelope → %d %v", code, out)
	}
	ls = a.ok(a.alice, "GET", "/api/v1/logs?group=device&q=memory", nil)["lines"].([]any)
	if len(ls) != 1 || ls[0].(map[string]any)["source_label"] != "Pocket AI iOS" || ls[0].(map[string]any)["level"] != "warning" {
		t.Fatalf("sentry log lines: %v", ls)
	}
}

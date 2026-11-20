package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	core "github.com/abjelosevic88/pocket-homelab-core/sdk"
	"github.com/abjelosevic88/pocket-homelab-errors/internal/config"
)

// moveLinks starts Errors behind a fake Core whose pcx_<who>_<export|import|read>
// tokens are Pocket Move links acting for alice@ or bob@.
func moveLinks(t *testing.T) *app {
	t.Helper()
	coreSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/api/v1/auth/introspect" {
			io.WriteString(w, `{}`)
			return
		}
		var in struct {
			Token string `json:"token"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		parts := strings.SplitN(strings.TrimPrefix(in.Token, "pcx_"), "_", 2)
		if len(parts) != 2 {
			io.WriteString(w, `{"active":false}`)
			return
		}
		scope := map[string]string{"export": core.ScopeMoveExport, "import": core.ScopeMoveImport, "read": "errors:read"}[parts[1]]
		email := parts[0] + "@example.com"
		fmt.Fprintf(w, `{"active":true,"kind":"delegated","scope":%q,"audience":"errors","user":{"id":1,"sub":%q,"email":%q,"name":%q,"role":"member"},"app":{"id":7,"slug":"move","name":"Pocket Move"}}`, scope, email, email, parts[0])
	}))
	t.Cleanup(coreSrv.Close)
	return newApp(t, func(c *config.Config) { c.CoreURL = coreSrv.URL })
}

func (a *app) send(tok, method, path, ctype string, body []byte) (int, []byte) {
	a.t.Helper()
	req, _ := http.NewRequest(method, a.url+path, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		a.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, b
}

func (a *app) moveImport(tok string, data []byte) map[string]any {
	a.t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "errors.json")
	_, _ = fw.Write(data)
	_ = mw.Close()
	code, out := a.send(tok, "POST", "/api/v1/import", mw.FormDataContentType(), buf.Bytes())
	if code != 200 {
		a.t.Fatalf("import → %d %s", code, out)
	}
	var m map[string]any
	_ = json.Unmarshal(out, &m)
	return m
}

func TestMoveLinkScopes(t *testing.T) {
	a := moveLinks(t)
	exp, imp, read := "pcx_bob_export", "pcx_bob_import", "pcx_bob_read"
	for _, c := range []struct {
		tok, method, path string
		want              int
	}{
		{exp, "GET", core.PortablePath, 200},
		{exp, "GET", "/api/v1/export.json", 200},
		{exp, "GET", "/api/v1/export.csv", 200},
		{exp, "POST", "/api/v1/import", 403},
		{exp, "GET", "/api/v1/settings", 403},
		{exp, "GET", "/api/v1/logs/export.txt", 403},
		{exp, "GET", "/api/v1/backup", 403},
		{exp, "GET", "/api/v1/users", 403},
		{imp, "GET", core.PortablePath, 200},
		{imp, "GET", "/api/v1/export.json", 403},
		{imp, "PUT", "/api/v1/settings", 403},
		{imp, "POST", "/api/v1/projects", 403},
		{imp, "POST", "/api/v1/report", 403},
		{read, "GET", core.PortablePath, 403},
		{read, "GET", "/api/v1/export.json", 403},
	} {
		if code, body := a.send(c.tok, c.method, c.path, "application/json", []byte("{}")); code != c.want {
			t.Errorf("%s %s %s → %d, want %d (%s)", c.tok, c.method, c.path, code, c.want, body)
		}
	}
	_, body := a.send(exp, "GET", core.PortablePath, "", nil)
	var p core.Portable
	if err := json.Unmarshal(body, &p); err != nil || len(p.Exports) != 2 || len(p.Imports) != 1 || p.Exports[0].Kind != core.KindPocket || p.Imports[0].Form != "file" {
		t.Fatalf("portable: %s", body)
	}
}

// Move brings Alice's errors to Bob: the import adds once, and nothing of
// one person shows up for the other.
func TestMoveImportsAddOnce(t *testing.T) {
	a := moveLinks(t)
	pid := id(project(a, a.alice, "Shop", "javascript"))
	a.ok(a.alice, "POST", "/api/v1/projects/"+pid+"/events", map[string]any{"type": "TypeError", "value": "x is undefined", "stack": "TypeError: x is undefined\n    at f (https://shop.test/a.js:1:2)"})
	a.ok(a.alice, "POST", "/api/v1/projects/"+pid+"/events", map[string]any{"message": "something else"})

	code, export := a.send("pcx_alice_export", "GET", "/api/v1/export.json", "", nil)
	if code != 200 || !strings.Contains(string(export), "x is undefined") {
		t.Fatalf("alice's export: %d %s", code, export)
	}
	if _, bob := a.send("pcx_bob_export", "GET", "/api/v1/export.json", "", nil); strings.Contains(string(bob), "Shop") {
		t.Fatalf("bob's export holds alice's errors: %s", bob)
	}

	first := a.moveImport("pcx_bob_import", export)
	if num(first, "projects") != 1 || num(first, "issues") != 2 || num(first, "events") != 2 {
		t.Fatalf("first import: %v", first)
	}
	second := a.moveImport("pcx_bob_import", export)
	if num(second, "projects")+num(second, "issues")+num(second, "events")+num(second, "links") != 0 {
		t.Fatalf("second import added: %v", second)
	}
	if l := a.ok(a.bob, "GET", "/api/v1/issues?status=all", nil)["issues"].([]any); len(l) != 2 {
		t.Fatalf("bob has %d issues", len(l))
	}
	if l := a.ok(a.alice, "GET", "/api/v1/projects", nil)["projects"].([]any); len(l) != 1 {
		t.Fatalf("alice's projects changed: %d", len(l))
	}
	if l := a.ok(a.alice, "GET", "/api/v1/issues?status=all", nil)["issues"].([]any); len(l) != 2 {
		t.Fatalf("alice's issues changed: %d", len(l))
	}
}

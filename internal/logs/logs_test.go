package logs

import (
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParse(t *testing.T) {
	cases := []struct {
		in    string
		level int
		msg   string
	}{
		{`time=2026-11-11T10:00:00Z level=ERROR msg="saving failed" err="disk full" user=3`, Error, "saving failed"},
		{`{"time":"x","level":"WARN","msg":"slow query","ms":812}`, Warn, "slow query"},
		{`{"level":30,"msg":"listening","port":80}`, Info, "listening"},
		{`2026-11-11 10:00:00,123 ERROR [worker] import broke`, Error, "ERROR [worker] import broke"},
		{`[2026-11-11T10:00:00Z] INFO started`, Info, "INFO started"},
		{`E1111 10:00:00.123456   1 main.go:42] boom`, Error, "boom"},
		{`panic: runtime error: index out of range`, Fatal, "panic: runtime error: index out of range"},
		{`just a line`, Info, "just a line"},
		{`DEBUG: cache warm`, Debug, "DEBUG: cache warm"},
		{`login with password=hunter22 ok`, Info, "login with password=•••• ok"},
	}
	for _, c := range cases {
		e := Parse(c.in, 0)
		if e.Level != c.level || e.Msg != c.msg {
			t.Errorf("%q → level %d msg %q, want %d %q", c.in, e.Level, e.Msg, c.level, c.msg)
		}
	}
	// a stack goes on at the level of the line before
	if e := Parse("\tmain.go:12 +0x1d", Error); e.Level != Error {
		t.Errorf("stack line level %d", e.Level)
	}
	caddy := `{"level":"error","ts":1794425023.0,"logger":"http.log.access.log1","msg":"handled request","request":{"client_ip":"10.0.0.1","method":"GET","host":"notes.bjelke.org:443","uri":"/api/v1/notes/12?token=abcdef123","headers":{"User-Agent":["curl/8"]}},"duration":0.0123,"size":10,"status":502}`
	e := Parse(caddy, 0)
	if e.Web == nil || e.Web.Host != "notes.bjelke.org" || e.Web.Status != 502 || e.Level != Error {
		t.Fatalf("caddy: %+v %+v", e, e.Web)
	}
	if strings.Contains(e.Msg, "abcdef123") || !strings.Contains(e.Msg, "→ 502") {
		t.Errorf("caddy msg %q", e.Msg)
	}
	if got := webTemplate(e.Web); got != "GET notes.bjelke.org/api/v1/notes/‹…› → 5xx" {
		t.Errorf("web template %q", got)
	}
}

func TestTemplate(t *testing.T) {
	a := Template(`user 42 from 10.0.0.1:5000 took 12ms (id 0b5e7c1d-4f1a-4c0e-9d1f-aa00bb11cc22)`)
	b := Template(`user 7 from 192.168.1.9:61000 took 380ms (id 11111111-2222-4333-8444-555555555555)`)
	if a != b {
		t.Errorf("%q != %q", a, b)
	}
	if !strings.Contains(a, "‹n›") || !strings.Contains(a, "‹ip›") || !strings.Contains(a, "‹id›") {
		t.Errorf("template %q", a)
	}
}

func TestFTSQuery(t *testing.T) {
	for in, want := range map[string]string{
		"disk full":      `"disk" AND "full"*`,
		`"/api/v1" -200`: `"api v1" NOT "200"`,
		"":               "",
		"-only":          "",
		"pocket-notes":   `"pocket notes"`,
	} {
		if got := FTSQuery(in); got != want {
			t.Errorf("%q → %q, want %q", in, got, want)
		}
	}
}

func TestStore(t *testing.T) {
	ctx := context.Background()
	d, err := Open(filepath.Join(t.TempDir(), "logs.db"), slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	a, _ := d.Ensure(ctx, SourceInfo{Kind: "docker", Name: "pocket-notes", Label: "Pocket Notes", Group: "pocket"})
	b, _ := d.Ensure(ctx, SourceInfo{Kind: "journal", Name: "user:pocket-ai.service", Label: "pocket-ai", Group: "user"})
	now := time.Now()
	var es []Entry
	for i := 0; i < 50; i++ {
		e := Parse(`level=INFO msg="saved note" id=`+itoa(int64(i)), 0)
		e.At, e.Source = now.Add(-time.Duration(50-i)*time.Minute), SourceKey("docker", "pocket-notes")
		es = append(es, e)
	}
	e := Parse(`level=ERROR msg="disk full" path=/data`, 0)
	e.At, e.Source = now.Add(-3*time.Minute), SourceKey("journal", "user:pocket-ai.service")
	es = append(es, e)
	if n, err := d.Write(ctx, es); err != nil || n != 51 {
		t.Fatalf("write %d %v", n, err)
	}
	ls, more, err := d.Lines(ctx, Filter{Q: "disk", Limit: 10})
	if err != nil || len(ls) != 1 || more || ls[0].Source != b.ID || ls[0].Level != "error" {
		t.Fatalf("search: %v %v %+v", err, more, ls)
	}
	ls, more, _ = d.Lines(ctx, Filter{Sources: []int64{a.ID}, Limit: 20})
	if len(ls) != 20 || !more {
		t.Fatalf("page: %d %v", len(ls), more)
	}
	ls2, _, _ := d.Lines(ctx, Filter{Sources: []int64{a.ID}, Before: ls[19].ID, Limit: 100})
	if len(ls2) != 30 {
		t.Fatalf("next page %d", len(ls2))
	}
	cx, err := d.Context(ctx, ls[5].ID, 3)
	if err != nil || len(cx) != 7 {
		t.Fatalf("context %d %v", len(cx), err)
	}
	o, err := d.Overview(ctx, now.Add(-24*time.Hour).UnixMilli(), now.UnixMilli())
	if err != nil || o.Totals["all"] != 51 || o.Totals["error"] != 1 || len(o.Sources) != 2 {
		t.Fatalf("overview %v %+v", err, o.Totals)
	}
	ps, all, err := d.Patterns(ctx, now.Add(-24*time.Hour).UnixMilli(), now.UnixMilli(), 0, "top", 10)
	if err != nil || all != 51 || len(ps) != 2 || ps[0].N != 50 {
		t.Fatalf("patterns %v %d %+v", err, all, ps)
	}
	// only errors kept: info lines are counted, not kept
	if _, err := d.SetKeep(ctx, a.ID, "error"); err != nil {
		t.Fatal(err)
	}
	e2 := Parse(`level=INFO msg="saved note"`, 0)
	e2.At, e2.Source = now, SourceKey("docker", "pocket-notes")
	if n, _ := d.Write(ctx, []Entry{e2}); n != 0 {
		t.Fatalf("kept %d", n)
	}
	// tidy: everything older than 0 days goes, the FTS rows too
	if n, err := d.Tidy(ctx, 0, 0); err != nil || n != 51 {
		t.Fatalf("tidy %d %v", n, err)
	}
	if ls, _, _ := d.Lines(ctx, Filter{Q: "disk"}); len(ls) != 0 {
		t.Fatalf("after tidy %d", len(ls))
	}
	w, err := d.WebTraffic(ctx, now.Add(-time.Hour).UnixMilli(), now.UnixMilli())
	if err != nil || w.Total != 0 {
		t.Fatal(err)
	}
}

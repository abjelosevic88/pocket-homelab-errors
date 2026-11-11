package server

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"sort"
	"strings"
	"time"

	"github.com/abjelosevic88/pocket-homelab-errors/internal/logs"
)

// seedDemoLogs writes two days of lines for the demo: a few Pocket apps,
// Caddy in front of them (with a bad half hour this morning), a database,
// Pocket AI and cron from the journal, and the kernel.
func (s *Server) seedDemoLogs(ctx context.Context) error {
	ctx = context.WithoutCancel(ctx)
	if s.logs == nil {
		return nil
	}
	if n, _, _ := s.logs.Counts(ctx); n > 0 {
		return nil
	}
	st := s.logSettings(ctx)
	st.On = true
	_ = s.saveLogSettings(ctx, st)
	r := rand.New(rand.NewSource(7))
	now := time.Now()
	start := now.Add(-48 * time.Hour)
	type src struct {
		si    logs.SourceInfo
		rate  float64 // lines a minute at the busiest
		lines func(at time.Time) string
	}
	pick := func(xs ...string) string { return xs[r.Intn(len(xs))] }
	slog := func(at time.Time, level, msg string, kv ...string) string {
		return fmt.Sprintf("time=%s level=%s msg=%q %s", at.UTC().Format(time.RFC3339Nano), level, msg, strings.Join(kv, " "))
	}
	bad := func(at time.Time) bool { // this morning's bad half hour
		d := now.Sub(at)
		return d > 5*time.Hour && d < 5*time.Hour+35*time.Minute
	}
	hosts := []string{"notes.example.home", "money.example.home", "photos.example.home", "core.example.home", "ai.example.home"}
	paths := []string{"/api/v1/notes", "/api/v1/notes/%d", "/api/v1/summary", "/assets/index-3f9a1c.js", "/api/v1/transactions?month=11", "/", "/api/v1/search?q=milk", "/healthz"}
	srcs := []src{
		{logs.SourceInfo{Kind: "docker", Name: "pocket-notes", Label: "Pocket Notes", Group: "pocket", Image: "pocket-notes:local"}, 6, func(at time.Time) string {
			switch x := r.Float64(); {
			case bad(at) && x < .3:
				return slog(at, "ERROR", "saving a note failed", "err=\"database is locked\"", fmt.Sprintf("note=%d", r.Intn(900)))
			case x < .02:
				return slog(at, "WARN", "slow query", fmt.Sprintf("ms=%d", 400+r.Intn(1500)), "table=notes")
			case x < .5:
				return slog(at, "INFO", "note saved", fmt.Sprintf("note=%d", r.Intn(900)), fmt.Sprintf("user=%d", 1+r.Intn(3)))
			default:
				return slog(at, "DEBUG", "http", "method=GET", fmt.Sprintf("path=/api/v1/notes/%d", r.Intn(900)), "status=200", fmt.Sprintf("dur=%dms", 2+r.Intn(40)))
			}
		}},
		{logs.SourceInfo{Kind: "docker", Name: "pocket-money", Label: "Pocket Money", Group: "pocket", Image: "pocket-money:local"}, 3, func(at time.Time) string {
			switch x := r.Float64(); {
			case x < .015:
				return slog(at, "ERROR", "bank sync failed", "bank=\"Raiffeisen\"", "err=\"401 Unauthorized\"")
			case x < .05:
				return slog(at, "WARN", "rate limited by the exchange-rate API", fmt.Sprintf("retry_in=%ds", 30+r.Intn(90)))
			default:
				return slog(at, "INFO", pick("transaction categorised", "receipt read", "budget checked"), fmt.Sprintf("id=%d", r.Intn(5000)))
			}
		}},
		{logs.SourceInfo{Kind: "docker", Name: "pocket-core", Label: "Pocket Core", Group: "pocket", Image: "pocket-core:local"}, 4, func(at time.Time) string {
			if r.Float64() < .01 {
				return slog(at, "WARN", "link token near its end", fmt.Sprintf("app=%s", pick("notes", "money", "tasks")))
			}
			return slog(at, "INFO", pick("event delivered", "resolve", "token refreshed", "outbox drained"), fmt.Sprintf("app=%s", pick("notes", "money", "tasks", "calendar")), fmt.Sprintf("ms=%d", 1+r.Intn(30)))
		}},
		{logs.SourceInfo{Kind: "docker", Name: "caddy", Label: "caddy", Group: "docker", Image: "caddy:2"}, 30, func(at time.Time) string {
			status := 200
			switch x := r.Float64(); {
			case bad(at) && x < .25:
				status = pick2(r, 502, 504)
			case x < .03:
				status = 404
			case x < .05:
				status = 304
			case x < .052:
				status = 500
			case x < .07:
				status = 401
			}
			ms := math.Abs(r.NormFloat64()*40 + 35)
			if r.Float64() < .01 || status == 504 {
				ms = 1000 + r.Float64()*9000
			}
			p := pick(paths...)
			if strings.Contains(p, "%d") {
				p = fmt.Sprintf(p, r.Intn(900))
			}
			level := "info"
			if status >= 500 {
				level = "error"
			}
			return fmt.Sprintf(`{"level":%q,"ts":%f,"logger":"http.log.access.log0","msg":"handled request","request":{"client_ip":"10.0.0.%d","proto":"HTTP/2.0","method":%q,"host":%q,"uri":%q,"headers":{"User-Agent":["Mozilla/5.0"]}},"duration":%f,"size":%d,"status":%d}`,
				level, float64(at.UnixNano())/1e9, 2+r.Intn(40), pick("GET", "GET", "GET", "POST", "PUT"), pick(hosts...), p, ms/1000, 200+r.Intn(90000), status)
		}},
		{logs.SourceInfo{Kind: "docker", Name: "immich_postgres", Label: "immich_postgres", Group: "docker", Image: "postgres:16"}, 1, func(at time.Time) string {
			if r.Float64() < .1 {
				return at.UTC().Format("2006-01-02 15:04:05.000 UTC") + " [1] LOG:  checkpoint complete: wrote " + fmt.Sprint(r.Intn(900)) + " buffers"
			}
			if r.Float64() < .03 {
				return at.UTC().Format("2006-01-02 15:04:05.000 UTC") + " [77] ERROR:  duplicate key value violates unique constraint \"asset_checksum\""
			}
			return at.UTC().Format("2006-01-02 15:04:05.000 UTC") + " [1] LOG:  automatic vacuum of table \"immich.public.assets\""
		}},
		{logs.SourceInfo{Kind: "journal", Name: "user:pocket-ai.service", Label: "pocket-ai", Group: "user"}, 5, func(at time.Time) string {
			switch x := r.Float64(); {
			case x < .01:
				return slog(at, "ERROR", "model call failed", "provider=anthropic", "err=\"overloaded\"")
			case x < .04:
				return slog(at, "WARN", "tool call took long", fmt.Sprintf("tool=pocket_%s", pick("notes_search", "money_summary", "tasks_list")), fmt.Sprintf("ms=%d", 3000+r.Intn(9000)))
			default:
				return slog(at, "INFO", pick("turn finished", "conversation started", "tool call", "stream closed"), fmt.Sprintf("conv=%d", r.Intn(400)), fmt.Sprintf("tokens=%d", r.Intn(9000)))
			}
		}},
		{logs.SourceInfo{Kind: "journal", Name: "cron.service", Label: "cron", Group: "system"}, .3, func(at time.Time) string {
			return fmt.Sprintf("(root) CMD (/usr/local/bin/backup-nightly --job %d)", 1+r.Intn(3))
		}},
		{logs.SourceInfo{Kind: "journal", Name: "kernel", Label: "kernel", Group: "kernel"}, .5, func(at time.Time) string {
			if r.Float64() < .2 {
				return fmt.Sprintf("audit: type=1400 audit(%d.%03d:%d): apparmor=\"DENIED\" operation=\"ptrace\" profile=\"docker-default\" pid=%d comm=\"ps\"", at.Unix(), r.Intn(999), r.Intn(99999), 1000+r.Intn(60000))
			}
			return fmt.Sprintf("br-%x: port %d(veth%x) entered forwarding state", r.Int63n(1<<40), 1+r.Intn(9), r.Int63n(1<<24))
		}},
	}
	var batch []logs.Entry
	for _, sc := range srcs {
		if _, err := s.logs.Ensure(ctx, sc.si); err != nil {
			return err
		}
		key := logs.SourceKey(sc.si.Kind, sc.si.Name)
		prev := 0
		for t := start; t.Before(now); t = t.Add(time.Minute) {
			h := float64(t.Hour()) + float64(t.Minute())/60
			day := .25 + .75*math.Max(0, math.Sin((h-6)/24*2*math.Pi*1.0+.3)) // quiet at night
			n := int(sc.rate*day + r.Float64())
			if bad(t) && sc.si.Name == "caddy" {
				n *= 2
			}
			for i := 0; i < n; i++ {
				at := t.Add(time.Duration(r.Int63n(int64(time.Minute))))
				e := logs.Parse(sc.lines(at), prev)
				if sc.si.Kind == "journal" && e.Level == logs.Info && strings.Contains(sc.si.Name, "kernel") && strings.Contains(e.Msg, "DENIED") {
					e.Level = logs.Warn
				}
				prev = e.Level
				e.At, e.Source = at, key
				batch = append(batch, e)
			}
		}
	}
	// in time order, as the collector writes them
	sort.SliceStable(batch, func(i, j int) bool { return batch[i].At.Before(batch[j].At) })
	for i := 0; i < len(batch); i += 5000 {
		if _, err := s.logs.Write(ctx, batch[i:min(i+5000, len(batch))]); err != nil {
			return err
		}
	}
	return nil
}

func pick2(r *rand.Rand, a, b int) int {
	if r.Intn(2) == 0 {
		return a
	}
	return b
}

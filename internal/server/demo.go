package server

import (
	"context"
	"fmt"
	"math/rand"
	"time"

	"github.com/abjelosevic88/pocket-homelab-errors/internal/intake"
	"github.com/abjelosevic88/pocket-homelab-errors/internal/store"
)

// ---- demo data (DEMO_DATA=true) ----
//
// The owner runs a small shop website, a booking API in Go, a nightly
// import in Python, and two Pocket apps whose container logs are read. A
// month of errors: some fixed, one back after it was fixed, one ignored,
// a busy one this morning. Nothing calls out.

type demoIssue struct {
	project int
	times   int
	days    int     // spread over the last … days
	recent  float64 // share in the last 24 hours
	status  string  // open | resolved | ignored | regressed
	event   func(r *rand.Rand) map[string]any
	log     []string // container lines instead of an event
}

func jsEvent(typ, val string, frames [][3]any, url string, crumbs []string) func(r *rand.Rand) map[string]any {
	return func(r *rand.Rand) map[string]any {
		fr := []any{}
		for _, f := range frames {
			fr = append(fr, map[string]any{"function": f[0], "filename": f[1], "lineno": f[2], "colno": 5 + r.Intn(30), "in_app": true})
		}
		fr = append([]any{map[string]any{"function": "dispatchEvent", "filename": "https://shop.example.com/assets/vendor-3f9a1c2b.js", "lineno": 1, "colno": 48211, "in_app": false}}, fr...)
		bc := []any{}
		for _, c := range crumbs {
			bc = append(bc, map[string]any{"category": "ui.click", "message": c})
		}
		bc = append(bc, map[string]any{"category": "navigation", "data": map[string]any{"from": "/", "to": url}})
		browsers := []string{"Chrome 130", "Safari 18.1", "Firefox 132", "Chrome Mobile 130", "Mobile Safari 18.1"}
		oses := []string{"Windows 11", "macOS 15", "iOS 18.1", "Android 14"}
		return map[string]any{
			"platform": "javascript", "level": "error", "release": "shop@2.4." + fmt.Sprint(1+r.Intn(3)), "environment": "production",
			"exception": map[string]any{"values": []any{map[string]any{"type": typ, "value": val, "mechanism": map[string]any{"type": "onerror", "handled": false},
				"stacktrace": map[string]any{"frames": fr}}}},
			"request":     map[string]any{"url": "https://shop.example.com" + url, "headers": map[string]any{"User-Agent": "Mozilla/5.0", "Cookie": "session=abc"}},
			"user":        map[string]any{"id": fmt.Sprintf("customer-%d", 100+r.Intn(60)), "ip_address": fmt.Sprintf("85.94.%d.%d", r.Intn(255), r.Intn(255))},
			"breadcrumbs": bc,
			"tags":        map[string]any{"browser": browsers[r.Intn(len(browsers))], "os": oses[r.Intn(len(oses))], "page": url},
			"contexts":    map[string]any{"browser": map[string]any{"name": browsers[r.Intn(len(browsers))]}, "os": map[string]any{"name": oses[r.Intn(len(oses))]}},
		}
	}
}

func goEvent(typ, val, txn string, frames [][3]any) func(r *rand.Rand) map[string]any {
	return func(r *rand.Rand) map[string]any {
		fr := []any{
			map[string]any{"function": "(*conn).serve", "module": "net/http", "filename": "/usr/local/go/src/net/http/server.go", "lineno": 2092, "in_app": false},
			map[string]any{"function": "(*Mux).ServeHTTP", "module": "github.com/go-chi/chi/v5", "filename": "/go/pkg/mod/github.com/go-chi/chi/v5@v5.1.0/mux.go", "lineno": 90, "in_app": false},
		}
		for _, f := range frames {
			fr = append(fr, map[string]any{"function": f[0], "module": "booking/internal/api", "filename": f[1], "lineno": f[2], "in_app": true,
				"context_line": "\t" + fmt.Sprint(f[0]) + "(ctx, req)"})
		}
		return map[string]any{
			"platform": "go", "level": "error", "release": "booking@1.8.0", "environment": "production", "transaction": txn, "server_name": "booking-1",
			"exception": []any{map[string]any{"type": typ, "value": val, "stacktrace": map[string]any{"frames": fr}}},
			"request":   map[string]any{"method": "POST", "url": "https://api.example.com" + txn[len("POST "):], "headers": map[string]any{"Authorization": "Bearer x", "Content-Type": "application/json"}},
			"tags":      map[string]any{"region": []string{"eu-central", "eu-west"}[r.Intn(2)]},
			"breadcrumbs": []any{
				map[string]any{"category": "db", "message": "SELECT * FROM slots WHERE day = $1"},
				map[string]any{"category": "http", "data": map[string]any{"method": "POST", "url": "https://payments.example.com/v1/charges", "status_code": 502}},
			},
		}
	}
}

func pyEvent(typ, val string, frames [][3]any) func(r *rand.Rand) map[string]any {
	return func(r *rand.Rand) map[string]any {
		fr := []any{}
		for _, f := range frames {
			fr = append(fr, map[string]any{"function": f[0], "filename": f[1], "lineno": f[2], "in_app": true,
				"pre_context": []any{"    rows = read_rows(path)", "    for row in rows:"}, "context_line": "        " + fmt.Sprint(f[0]) + "(row)", "post_context": []any{"    db.commit()"}})
		}
		return map[string]any{
			"platform": "python", "level": "error", "release": "import@2026.11", "environment": "production", "logger": "nightly_import",
			"exception": map[string]any{"values": []any{map[string]any{"type": typ, "value": val, "module": "builtins", "stacktrace": map[string]any{"frames": fr}}}},
			"extra":     map[string]any{"file": fmt.Sprintf("orders-2026-11-%02d.csv", 1+r.Intn(7)), "row": 1200 + r.Intn(800), "password": "never kept"},
		}
	}
}

var demoProjects = []store.Project{
	{Name: "Shop website", Platform: "javascript", NotifyNew: true, NotifyAgain: true, NotifySpike: 50},
	{Name: "Booking API", Platform: "go", NotifyNew: true, NotifyAgain: true},
	{Name: "Nightly import", Platform: "python", NotifyNew: true, NotifyAgain: true},
	{Name: "Pocket Notes", Platform: "docker", Source: "docker", Container: "pocket-notes", AppSlug: "notes", NotifyNew: true, NotifyAgain: true},
	{Name: "Pocket Money", Platform: "docker", Source: "docker", Container: "pocket-money", AppSlug: "money", NotifyNew: true, NotifyAgain: true},
}

var demoIssues = []demoIssue{
	{project: 0, times: 412, days: 20, recent: 0.35, status: "open", event: jsEvent("TypeError", "Cannot read properties of undefined (reading 'price')",
		[][3]any{{"renderCart", "https://shop.example.com/assets/cart-8d1e44a0.js", 214}, {"CartRow", "https://shop.example.com/assets/cart-8d1e44a0.js", 88}, {"lineTotal", "https://shop.example.com/assets/money-1b2c3d4e.js", 12}},
		"/cart", []string{"button.add-to-cart \"Add to cart\"", "a.cart-link \"Cart (2)\""})},
	{project: 0, times: 96, days: 2, recent: 0.9, status: "open", event: jsEvent("ChunkLoadError", "Loading chunk 412 failed. (timeout: https://shop.example.com/assets/checkout-5a6b7c8d.js)",
		[][3]any{{"loadCheckout", "https://shop.example.com/assets/app-0f1e2d3c.js", 1180}, {"requireEnsure", "https://shop.example.com/assets/app-0f1e2d3c.js", 64}},
		"/checkout", []string{"button.checkout \"Go to checkout\""})},
	{project: 0, times: 37, days: 25, recent: 0.05, status: "resolved", event: jsEvent("RangeError", "Invalid time value",
		[][3]any{{"formatDelivery", "https://shop.example.com/assets/delivery-77aa88bb.js", 41}, {"DeliveryDate", "https://shop.example.com/assets/delivery-77aa88bb.js", 19}},
		"/product/linen-shirt", nil)},
	{project: 0, times: 18, days: 12, recent: 0.2, status: "regressed", event: jsEvent("TypeError", "navigator.clipboard is undefined",
		[][3]any{{"copyCode", "https://shop.example.com/assets/coupon-12ab34cd.js", 9}},
		"/account/coupons", []string{"button.copy \"Copy code\""})},
	{project: 0, times: 240, days: 30, recent: 0.03, status: "ignored", event: jsEvent("Error", "ResizeObserver loop completed with undelivered notifications.",
		nil, "/", nil)},
	{project: 1, times: 58, days: 6, recent: 0.4, status: "open", event: goEvent("*errors.errorString", "payment provider: 502 Bad Gateway", "POST /v1/bookings",
		[][3]any{{"(*Handler).CreateBooking", "/src/internal/api/bookings.go", 118}, {"(*Payments).Charge", "/src/internal/api/payments.go", 64}})},
	{project: 1, times: 9, days: 14, recent: 0, status: "resolved", event: goEvent("*pq.Error", "duplicate key value violates unique constraint \"slots_day_time_key\"", "POST /v1/slots",
		[][3]any{{"(*Handler).CreateSlot", "/src/internal/api/slots.go", 77}, {"(*Store).InsertSlot", "/src/internal/store/slots.go", 41}})},
	{project: 2, times: 6, days: 7, recent: 0.15, status: "open", event: pyEvent("KeyError", "'customer_email'",
		[][3]any{{"main", "/srv/import/run.py", 31}, {"import_file", "/srv/import/orders.py", 88}, {"to_order", "/srv/import/orders.py", 142}})},
	{project: 2, times: 2, days: 20, recent: 0, status: "resolved", event: pyEvent("UnicodeDecodeError", "'utf-8' codec can't decode byte 0x9a in position 1043: invalid start byte",
		[][3]any{{"main", "/srv/import/run.py", 31}, {"read_rows", "/srv/import/files.py", 22}})},
	{project: 3, times: 23, days: 9, recent: 0.25, status: "open", log: []string{
		`time=2026-11-07T09:12:44.120+01:00 level=ERROR msg="save note" err="database is locked" id=812 user=1`,
	}},
	{project: 3, times: 1, days: 3, recent: 0, status: "open", log: []string{
		`panic: runtime error: index out of range [3] with length 3`,
		``,
		`goroutine 418 [running]:`,
		`github.com/abjelosevic88/pocket-homelab-notes/internal/server.(*Server).handleBacklinks(0xc0001c2000, {0x1a2b3c0, 0xc0004d6000}, 0xc000512000)`,
		"\t/src/internal/server/links.go:233 +0x5b4",
		`net/http.HandlerFunc.ServeHTTP(...)`,
		"\t/usr/local/go/src/net/http/server.go:2220",
		`github.com/go-chi/chi/v5.(*Mux).routeHTTP(0xc0001b4000, {0x1a2b3c0, 0xc0004d6000}, 0xc000512000)`,
		"\t/go/pkg/mod/github.com/go-chi/chi/v5@v5.2.1/mux.go:459 +0x2c6",
	}},
	{project: 4, times: 11, days: 16, recent: 0.1, status: "open", log: []string{
		`time=2026-11-07T07:30:02.001+01:00 level=ERROR msg="bank sync" bank="Raiffeisen" err="Post \"https://api.bank.example/v2/token\": context deadline exceeded"`,
	}},
	{project: 4, times: 4, days: 22, recent: 0, status: "resolved", log: []string{
		`time=2026-11-07T07:30:02.001+01:00 level=ERROR msg="recurring bill" bill=44 err="next date: day 31 does not exist in month 11"`,
	}},
}

// demoExplanation is what Pocket AI said about the busiest issue (so the
// demo shows the panel without asking anything).
const demoExplanation = "**What happened**\nThe cart page tried to read `price` from a product that was not there, so the whole cart stopped drawing.\n\n" +
	"**Likely cause**\nA product in someone's cart was removed or hidden after they added it; `lineTotal` still expects every row to have its product.\n\n" +
	"**Where to look**\n`lineTotal` in `money.js` (line 12), called from `CartRow` and `renderCart` in `cart.js`.\n\n" +
	"**How to fix**\n1. In `CartRow`, skip (or show as \"no longer available\") rows whose product is missing.\n2. Make `lineTotal` return 0 when `item.product` is undefined.\n3. When a product is removed, drop it from saved carts on the next load."

func (s *Server) seedDemo(ctx context.Context, uid int64) error {
	ctx = context.WithoutCancel(ctx)
	if n := s.store.CountsFor(ctx, uid)["projects"]; n > 0 {
		return nil
	}
	r := rand.New(rand.NewSource(42))
	now := time.Now()
	var projects []*store.Project
	for _, dp := range demoProjects {
		p := dp
		p.UserID = uid
		if err := s.store.CreateProject(ctx, &p); err != nil {
			return err
		}
		projects = append(projects, &p)
	}
	ids := make([]int64, len(demoIssues))
	for _, di := range demoIssues {
		p := projects[di.project]
		for i := 0; i < di.times; i++ {
			var at time.Time
			if r.Float64() < di.recent {
				at = now.Add(-time.Duration(r.Int63n(int64(20 * time.Hour))))
			} else {
				at = now.Add(-time.Duration(24*time.Hour) - time.Duration(r.Int63n(int64(time.Duration(di.days)*24*time.Hour))))
			}
			// working hours are busier
			if h := at.Hour(); h < 7 && r.Intn(3) > 0 {
				at = at.Add(time.Duration(8+r.Intn(4)) * time.Hour)
				if at.After(now) {
					at = now.Add(-time.Duration(r.Intn(50)+1) * time.Minute)
				}
			}
			if i == di.times-1 && di.status == "regressed" {
				continue
			}
			ev := s.demoEvent(r, p, di, at)
			if _, err := s.recordDemo(ctx, p, ev); err != nil {
				return err
			}
		}
	}
	// statuses, the issue that came back, the explanation and a link
	list, _, err := s.store.ListIssues(ctx, uid, store.IssueFilter{Status: "all", Sort: "first", Limit: 100})
	if err != nil {
		return err
	}
	byTitle := map[string]int64{}
	for _, is := range list {
		byTitle[is.Title] = is.ID
	}
	for k, di := range demoIssues {
		ev := s.demoEvent(r, projects[di.project], di, now)
		ids[k] = byTitle[intake.Title(&ev.Data)]
		id := ids[k]
		if id == 0 {
			continue
		}
		switch di.status {
		case "resolved":
			_, _ = s.store.SetStatus(ctx, uid, []int64{id}, "resolved", "")
		case "ignored":
			_, _ = s.store.SetStatus(ctx, uid, []int64{id}, "ignored", "")
		case "regressed":
			_, _ = s.store.SetStatus(ctx, uid, []int64{id}, "resolved", "")
			ev.At = now.Add(-35 * time.Minute)
			ev.EventID = ""
			if _, err := s.recordDemo(ctx, projects[di.project], ev); err != nil {
				return err
			}
		}
	}
	if ids[0] > 0 {
		_ = s.store.SetAI(ctx, uid, ids[0], demoExplanation)
		_ = s.store.SetNote(ctx, uid, ids[0], "Happens since the spring sale removed old products. Ana is on it.")
		_ = s.store.AddLink(ctx, uid, &store.IssueLink{IssueID: ids[0], App: "projects", Ref: "SHOP-142", Title: "Cart crashes when a product was removed", URL: ""})
	}
	return nil
}

func (s *Server) demoEvent(r *rand.Rand, p *store.Project, di demoIssue, at time.Time) *intake.Event {
	if len(di.log) > 0 {
		rd := &intake.LogReader{Container: p.Container}
		var evs []*intake.Event
		for _, l := range di.log {
			evs = append(evs, rd.Line(at, l)...)
		}
		if ev := rd.Flush(); ev != nil {
			evs = append(evs, ev)
		}
		ev := evs[0]
		ev.Data.ServerName = p.Container
		ev.Data.Tags = append([][2]string{{"container", p.Container}, {"image", p.Container + ":local"}}, ev.Data.Tags...)
		return ev
	}
	ev := intake.FromMap(di.event(r))
	ev.At = at
	ev.EventID = intake.NewEventID()
	return ev
}

// recordDemo files a demo event like ingest does, without telling anyone.
func (s *Server) recordDemo(ctx context.Context, p *store.Project, ev *intake.Event) (store.RecordResult, error) {
	ip := ipOf(ev)
	scrub(&ev.Data, true)
	if ip != "" && ev.Who == ip {
		ev.Who = hashWho(ip)
	}
	d := &ev.Data
	return s.store.Record(ctx, p, store.Incoming{EventID: ev.EventID, At: ev.At, Fingerprint: intake.Fingerprint(p.ID, d), Title: intake.Title(d),
		Culprit: intake.Culprit(d), Type: intake.Type(d), Level: ev.Level, Message: clip(firstNonEmpty(d.Message, mainValue(d)), 1000),
		Environment: ev.Environment, Release: ev.Release, Platform: firstNonEmpty(ev.Platform, p.Platform), Who: ev.Who, Data: *d})
}

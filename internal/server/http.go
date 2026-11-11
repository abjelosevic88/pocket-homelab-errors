package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	core "github.com/abjelosevic88/pocket-homelab-core/sdk"
	"github.com/abjelosevic88/pocket-homelab-errors/internal/store"
)

// Router builds the chi router.
func (s *Server) Router() http.Handler {
	r := chi.NewRouter()
	if s.cfg.TrustProxy {
		r.Use(middleware.RealIP)
	}
	r.Use(middleware.RequestID)
	r.Use(s.logging)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Compress(5))

	r.Get("/pocket-ask.js", s.core.AskWidget("errors"))
	r.Get("/errors.js", s.handleErrorsJS)
	r.Get("/pocket-logs.sh", s.handleForwardScript)

	// Sentry's door: any Sentry SDK sends here with the project's DSN
	for _, kind := range []string{"store", "envelope"} {
		h := s.handleSentryStore
		if kind == "envelope" {
			h = s.handleSentryEnvelope
		}
		for _, slash := range []string{"/", ""} {
			path := "/api/{pid:[0-9]+}/" + kind + slash
			r.Options(path, s.handleSentryPreflight)
			r.Post(path, h)
		}
	}
	r.Get("/healthz", s.handleHealth)
	r.Get("/readyz", s.handleReady)
	r.Get("/api/health", s.handleHealth)
	if s.cfg.MetricsEnabled {
		r.Get("/metrics", s.handleMetrics)
	}

	r.Route("/api/v1", func(r chi.Router) {
		// imports, AI and the other apps can take longer than a request may
		r.Group(func(r chi.Router) {
			r.Use(s.requireAuth)
			r.Post("/import", s.handleImport)
			r.Post("/issues/{id}/explain", s.handleExplain)
			r.Post("/ai/digest", s.handleDigest)
			r.Post("/issues/{id}/send", s.handleSendIssue)
			r.Get("/projects-teams", s.handleTeams)
		})

		r.Group(func(r chi.Router) {
			r.Use(middleware.Timeout(120 * time.Second))
			r.Post("/auth/login", s.handleLogin)
			r.Post("/auth/setup", s.handleSetup)
			r.Get("/auth/status", s.handleAuthStatus)
			r.Get("/auth/oidc/start", s.handleOIDCStart)
			r.Get("/auth/oidc/callback", s.handleOIDCCallback)

			r.Group(func(r chi.Router) {
				r.Use(s.requireAuth)
				r.Post("/auth/logout", s.handleLogout)
				r.Get("/auth/me", s.handleMe)
				r.Put("/auth/me", s.handleUpdateMe)

				r.Get("/summary", s.handleSummary)
				r.Get("/search", s.handleSearch)
				r.Get("/day/{date}", s.handleDay)

				r.Get("/issues", s.handleListIssues)
				r.Post("/issues/status", s.handleIssuesStatus)
				r.Get("/issues/{id}", s.handleGetIssue)
				r.Put("/issues/{id}", s.handleUpdateIssue)
				r.Delete("/issues/{id}", s.handleDeleteIssue)
				r.Post("/issues/{id}/status", s.handleIssueStatus)
				r.Get("/issues/{id}/hours", s.handleIssueHours)
				r.Get("/issues/{id}/events", s.handleListEvents)
				r.Get("/issues/{id}/latest", s.handleGetEvent)
				r.Get("/issues/{id}/events/{eid}", s.handleGetEvent)
				r.Get("/issues/{id}/markdown", s.handleIssueMarkdown)
				r.Delete("/issues/{id}/links/{lid}", s.handleDeleteLink)

				r.Get("/projects", s.handleListProjects)
				r.Post("/projects", s.handleCreateProject)
				r.Get("/projects/{id}", s.handleGetProject)
				r.Put("/projects/{id}", s.handleUpdateProject)
				r.Delete("/projects/{id}", s.handleDeleteProject)
				r.Post("/projects/{id}/events", s.handleProjectEvent)
				r.Post("/projects/{id}/test", s.handleTestEvent)
				r.Post("/report", s.handleReport)
				r.Get("/watch", s.handleWatch)

				r.Get("/logs", s.handleLogLines)
				r.Get("/logs/status", s.handleLogsStatus)
				r.Put("/logs/settings", s.handlePutLogsSettings)
				r.Post("/logs/clear", s.handleLogsClear)
				r.Post("/logs/push", s.handleLogsPush)
				r.Get("/logs/histogram", s.handleLogHistogram)
				r.Get("/logs/overview", s.handleLogOverview)
				r.Get("/logs/patterns", s.handleLogPatterns)
				r.Get("/logs/web", s.handleLogWeb)
				r.Get("/logs/sources", s.handleLogSources)
				r.Put("/logs/sources/{id}", s.handlePutLogSource)
				r.Get("/logs/lines/{id}/context", s.handleLogContext)
				r.Get("/logs/export.txt", s.handleLogExport)
				r.Get("/issues/{id}/logs", s.handleIssueLogs)

				r.Get("/apps", s.handleApps)

				r.Get("/ai/status", s.handleAIStatus)
				r.Get("/ai/models", s.handleAIModels)

				r.Post("/integrations/test", s.handleIntegrationTest)

				r.Get("/users", s.handleListUsers)
				r.Post("/users", s.handleCreateUser)
				r.Put("/users/{id}", s.handleUpdateUser)
				r.Delete("/users/{id}", s.handleDeleteUser)

				r.Get("/settings", s.handleGetSettings)
				r.Put("/settings", s.handlePutSettings)
				r.Get("/settings/system", s.handleSystemInfo)

				r.Get("/tokens", s.handleListTokens)
				r.Post("/tokens", s.handleCreateToken)
				r.Delete("/tokens/{id}", s.handleDeleteToken)

				r.Get("/backup", s.handleBackup)
				r.Get("/export.json", s.handleExportJSON)
				r.Get("/export.csv", s.handleExportCSV)
			})
		})
	})

	r.NotFound(s.handleSPA)
	return r
}

// ---------- helpers ----------

func (s *Server) logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)
		s.requests.Add(1)
		if ww.Status() >= 500 {
			s.errors.Add(1)
		}
		if strings.HasPrefix(r.URL.Path, "/assets/") {
			return
		}
		s.log.Debug("http", "method", r.Method, "path", r.URL.Path, "status", ww.Status(), "dur", time.Since(start).String(), "ip", r.RemoteAddr)
	})
}

type apiError struct {
	Error string `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, apiError{Error: msg})
}

func (s *Server) fail(w http.ResponseWriter, err error, context string) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeErr(w, http.StatusNotFound, "not found")
		return
	case errors.Is(err, store.ErrConflict), errors.Is(err, core.ErrOperationConflict):
		writeErr(w, http.StatusConflict, "already exists")
		return
	}
	s.log.Error(context, "err", err)
	writeErr(w, http.StatusInternalServerError, context+": "+err.Error())
}

func (s *Server) readBody(r *http.Request) ([]byte, error) {
	return io.ReadAll(http.MaxBytesReader(nil, r.Body, s.cfg.MaxBodyBytes))
}

func decode(r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 32<<20))
	return dec.Decode(v)
}

// decodeKeys decodes JSON into v and also reports which top-level keys were
// present, so PUT handlers can tell "not sent" from "sent empty".
func decodeKeys(r *http.Request, v any) (map[string]bool, error) {
	b, err := io.ReadAll(http.MaxBytesReader(nil, r.Body, 32<<20))
	if err != nil {
		return nil, err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, err
	}
	keys := make(map[string]bool, len(raw))
	for k := range raw {
		keys[k] = true
	}
	return keys, json.Unmarshal(b, v)
}

func idParam(r *http.Request, name string) int64 {
	n, _ := strconv.ParseInt(chi.URLParam(r, name), 10, 64)
	return n
}

func queryInt(r *http.Request, name string) int {
	n, _ := strconv.Atoi(r.URL.Query().Get(name))
	return n
}

func queryBool(r *http.Request, name string) bool {
	v := strings.ToLower(r.URL.Query().Get(name))
	return v == "1" || v == "true" || v == "yes"
}

func validDate(s string) bool {
	if s == "" {
		return true
	}
	_, err := time.Parse("2006-01-02", s)
	return err == nil
}

// ---------- health / metrics ----------

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"core_outbox": s.coreOutboxHealth(r.Context()), "status": "ok", "version": Version, "uptime": time.Since(s.started).Round(time.Second).String()})
}

func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DB.PingContext(r.Context()); err != nil {
		writeErr(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	counts := s.store.Counts(ctx)
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	fmt.Fprintf(w, "# HELP pocket_errors_info Build info.\n# TYPE pocket_errors_info gauge\npocket_errors_info{version=%q} 1\n", Version)
	fmt.Fprintf(w, "# HELP pocket_errors_uptime_seconds Process uptime.\n# TYPE pocket_errors_uptime_seconds gauge\npocket_errors_uptime_seconds %d\n", int(time.Since(s.started).Seconds()))
	fmt.Fprintf(w, "# HELP pocket_errors_http_requests_total HTTP requests served.\n# TYPE pocket_errors_http_requests_total counter\npocket_errors_http_requests_total %d\n", s.requests.Load())
	fmt.Fprintf(w, "# HELP pocket_errors_http_errors_total HTTP 5xx responses.\n# TYPE pocket_errors_http_errors_total counter\npocket_errors_http_errors_total %d\n", s.errors.Load())
	fmt.Fprintf(w, "# HELP pocket_errors_rows Rows per table.\n# TYPE pocket_errors_rows gauge\n")
	for k, v := range counts {
		fmt.Fprintf(w, "pocket_errors_rows{table=%q} %d\n", k, v)
	}
}

// ---------- static / SPA ----------

func (s *Server) handleSPA(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if s.webFS == nil {
		http.Error(w, "web UI not built (run `make web`)", http.StatusNotFound)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/")
	if path == "" {
		path = "index.html"
	}
	if f, err := s.webFS.Open(path); err == nil {
		f.Close()
		if strings.HasPrefix(path, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		http.FileServerFS(s.webFS).ServeHTTP(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
	r.URL.Path = "/"
	http.FileServerFS(s.webFS).ServeHTTP(w, r)
}

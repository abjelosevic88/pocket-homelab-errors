package server

import (
	"net/http"
	"strings"

	"github.com/abjelosevic88/pocket-homelab-errors/internal/store"
)

// ---- projects ----

// projectView is a project with the addresses an app sends to.
type projectView struct {
	*store.Project
	DSN       string `json:"dsn"`        // the Sentry DSN at the public address
	DSNLocal  string `json:"dsn_local"`  // the same for apps on this box, when that differs
	StoreURL  string `json:"store_url"`  // the plain JSON API for a token
	ScriptURL string `json:"script_url"` // the small browser script
}

func (s *Server) view(p *store.Project) projectView {
	v := projectView{Project: p, DSN: dsn(s.cfg.BaseURL, p), StoreURL: s.link("/api/v1/projects/" + itoa(p.ID) + "/events"),
		ScriptURL: s.link("/errors.js")}
	if s.cfg.IngestURL != "" && s.cfg.IngestURL != s.cfg.BaseURL {
		v.DSNLocal = dsn(s.cfg.IngestURL, p)
	}
	return v
}

func (s *Server) handleListProjects(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	list, err := s.store.ListProjects(ctx, actorID(ctx), queryBool(r, "archived"))
	if err != nil {
		s.fail(w, err, "projects")
		return
	}
	out := make([]projectView, 0, len(list))
	for _, p := range list {
		out = append(out, s.view(p))
	}
	writeJSON(w, http.StatusOK, map[string]any{"projects": out})
}

func (s *Server) handleGetProject(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p, err := s.store.GetProject(ctx, actorID(ctx), idParam(r, "id"))
	if err != nil {
		s.fail(w, err, "project")
		return
	}
	writeJSON(w, http.StatusOK, s.view(p))
}

// projectIn is what a person can set on a project.
type projectIn struct {
	Name        *string `json:"name"`
	Platform    *string `json:"platform"`
	AppSlug     *string `json:"app_slug"`
	NotifyNew   *bool   `json:"notify_new"`
	NotifyAgain *bool   `json:"notify_again"`
	NotifySpike *int    `json:"notify_spike"`
	MinLevel    *string `json:"min_level"`
	RateLimit   *int    `json:"rate_limit"`
	Archived    *bool   `json:"archived"`
	NewKey      bool    `json:"new_key"`
}

func (in projectIn) apply(p *store.Project) error {
	if in.Name != nil {
		p.Name = clip(strings.TrimSpace(*in.Name), 120)
	}
	if p.Name == "" {
		return &userErr{"give the project a name"}
	}
	if in.Platform != nil {
		if !oneOf(*in.Platform, store.Platforms) {
			return &userErr{"the platform is javascript, go, python, node, docker or other"}
		}
		p.Platform = *in.Platform
	}
	if in.AppSlug != nil {
		p.AppSlug = clip(strings.ToLower(strings.TrimSpace(*in.AppSlug)), 60)
	}
	if in.NotifyNew != nil {
		p.NotifyNew = *in.NotifyNew
	}
	if in.NotifyAgain != nil {
		p.NotifyAgain = *in.NotifyAgain
	}
	if in.NotifySpike != nil {
		if *in.NotifySpike < 0 || *in.NotifySpike > 100000 {
			return &userErr{"a busy hour is 0 (never told) to 100000 errors"}
		}
		p.NotifySpike = *in.NotifySpike
	}
	if in.MinLevel != nil {
		if !oneOf(*in.MinLevel, []string{"error", "warning"}) {
			return &userErr{"read a container's errors, or its warnings too"}
		}
		p.MinLevel = *in.MinLevel
	}
	if in.RateLimit != nil {
		if *in.RateLimit < 0 || *in.RateLimit > 100000 {
			return &userErr{"keep 0 (the usual 600) to 100000 errors a minute"}
		}
		p.RateLimit = *in.RateLimit
	}
	if in.Archived != nil {
		p.Archived = *in.Archived
	}
	if in.NewKey {
		p.PublicKey = store.NewKey()
	}
	return nil
}

func (s *Server) handleCreateProject(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var in projectIn
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	p := &store.Project{UserID: actorID(ctx), Platform: "javascript", NotifyNew: true, NotifyAgain: true, MinLevel: "error"}
	if err := in.apply(p); err != nil {
		s.userFail(w, err, "project")
		return
	}
	if err := s.store.CreateProject(ctx, p); err != nil {
		s.fail(w, err, "project")
		return
	}
	writeJSON(w, http.StatusCreated, s.view(p))
}

func (s *Server) handleUpdateProject(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p, err := s.store.GetProject(ctx, actorID(ctx), idParam(r, "id"))
	if err != nil {
		s.fail(w, err, "project")
		return
	}
	var in projectIn
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if err := in.apply(p); err != nil {
		s.userFail(w, err, "project")
		return
	}
	if err := s.store.UpdateProject(ctx, p); err != nil {
		s.fail(w, err, "project")
		return
	}
	writeJSON(w, http.StatusOK, s.view(p))
}

func (s *Server) handleDeleteProject(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if err := s.store.DeleteProject(ctx, actorID(ctx), idParam(r, "id")); err != nil {
		s.fail(w, err, "project")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleTestEvent sends a made-up error to a project, to see it arrive.
func (s *Server) handleTestEvent(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p, err := s.store.GetProject(ctx, actorID(ctx), idParam(r, "id"))
	if err != nil {
		s.fail(w, err, "project")
		return
	}
	ev := testEvent(p)
	res, err := s.ingest(ctx, p, ev)
	if err != nil {
		s.fail(w, err, "test")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"issue_id": res.Issue.ID, "outcome": res.Outcome})
}

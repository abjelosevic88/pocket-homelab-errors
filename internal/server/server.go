// Package server wires the HTTP API, auth and the embedded SPA.
package server

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/bcrypt"

	core "github.com/abjelosevic88/pocket-homelab-core/sdk"
	"github.com/abjelosevic88/pocket-homelab-errors/internal/config"
	"github.com/abjelosevic88/pocket-homelab-errors/internal/store"
)

// Version is injected at build time.
var Version = "dev"

// Server holds dependencies.
type Server struct {
	cfg      *config.Config
	store    *store.Store
	log      *slog.Logger
	webFS    fs.FS
	started  time.Time
	requests atomic.Int64
	errors   atomic.Int64

	aiSem chan struct{} // AI help running at once

	rateMu sync.Mutex
	rate   map[int64]*minuteCount // events a minute per project
	toldMu sync.Mutex
	told   map[int64]*minuteCount // notifications an hour per person

	docker  *dockerClient
	watchMu sync.Mutex
	watch   map[int64]*watchState // per person: the containers read and how it went

	oidcMu sync.Mutex
	oidc   *oidcProvider // for the issuer in use; rebuilt when it changes

	core     *core.Client
	coreMu   sync.Mutex
	coreOK   bool
	coreErr  string
	resolved map[string]resolvedURL
	modesMu  sync.Mutex // one connection-mode report at a time

	statusMu    sync.Mutex
	statusCache map[string]cachedState
}

type resolvedURL struct {
	url, public string
	at          time.Time
}

// New creates a server.
func New(cfg *config.Config, st *store.Store, log *slog.Logger, webFS fs.FS) *Server {
	return &Server{cfg: cfg, store: st, log: log, webFS: webFS, started: time.Now(), oidc: &oidcProvider{},
		core:     core.New(cfg.CoreURL, core.Options{DB: st.DB, TokenFile: cfg.DataDir + "/core.token", UserAgent: cfg.UserAgent}),
		resolved: map[string]resolvedURL{}, aiSem: make(chan struct{}, 3), statusCache: map[string]cachedState{},
		rate: map[int64]*minuteCount{}, told: map[int64]*minuteCount{}, watch: map[int64]*watchState{},
		docker: newDockerClient(cfg.DockerSocket)}
}

// Bootstrap seeds the first admin (if configured), the owner's integration
// settings from the environment and demo data, then starts the background work.
func (s *Server) Bootstrap(ctx context.Context) error {
	if err := s.core.Start(ctx); err != nil {
		return err
	}
	n, err := s.store.CountUsers(ctx)
	if err != nil {
		return err
	}
	if n == 0 && s.cfg.AuthMode == "local" && s.cfg.AdminEmail != "" && s.cfg.AdminPassword != "" {
		if len(s.cfg.AdminPassword) < 8 {
			return errors.New("ADMIN_PASSWORD must be at least 8 characters")
		}
		h, _ := bcrypt.GenerateFromPassword([]byte(s.cfg.AdminPassword), bcrypt.DefaultCost)
		u := &store.User{Email: s.cfg.AdminEmail, Name: s.cfg.AdminName, PasswordHash: string(h), Role: "admin"}
		if err := s.store.CreateUser(ctx, u); err != nil {
			return err
		}
		st, _ := s.store.GetSettings(ctx, 0)
		st.SetupComplete = true
		if err := s.store.SaveBoxSettings(ctx, st); err != nil {
			return err
		}
		s.log.Info("admin user created from environment", "email", u.Email)
	}
	if owner := s.store.OwnerID(ctx); owner > 0 {
		if err := s.ownerReady(ctx, owner); err != nil {
			return err
		}
	}
	_ = s.store.PurgeSessions(ctx)
	go s.janitor(ctx)
	go s.watchLoop(ctx)
	if s.core.Configured() {
		go s.coreLoop(ctx)
	}
	return nil
}

// ownerReady runs once the box owner (the first admin) exists: the
// integration settings from the environment are the owner's — seeded into the
// owner's personal settings only, never into anyone else's — and demo data
// lands in the owner's account.
func (s *Server) ownerReady(ctx context.Context, owner int64) error {
	st, err := s.store.GetSettings(ctx, owner)
	if err != nil {
		return err
	}
	changed := false
	once := func(name string, empty bool, v string, apply func()) {
		key := fmt.Sprintf("seeded:%d:%s", owner, name)
		if _, _, done := s.store.CacheGet(ctx, key); done {
			return
		}
		if empty && v != "" {
			apply()
			changed = true
			empty = false
		}
		if !empty {
			_ = s.store.CachePut(ctx, key, "1")
		}
	}
	set := func(name string, dst *string, v string) {
		once(name, *dst == "", v, func() { *dst = v })
	}
	set("OpenAIKey", &st.OpenAIKey, s.cfg.OpenAIKey)
	set("OpenAIModel", &st.OpenAIModel, s.cfg.OpenAIModel)
	set("OpenAIBaseURL", &st.OpenAIBaseURL, s.cfg.OpenAIBaseURL)
	set("NotifyURL", &st.NotifyURL, s.cfg.NotifyURL)
	set("NotifyFormat", &st.NotifyFormat, s.cfg.NotifyFormat)
	if changed {
		if err := s.store.SaveUserSettings(ctx, owner, st); err != nil {
			return err
		}
	}
	if s.cfg.DemoData {
		if err := s.seedDemo(ctx, owner); err != nil {
			return err
		}
	}
	return nil
}

// janitor tidies up once an hour: old sessions, and everybody's events
// older than the days they keep.
func (s *Server) janitor(ctx context.Context) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	s.tidy(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_ = s.store.PurgeSessions(ctx)
			s.tidy(ctx)
		}
	}
}

func (s *Server) tidy(ctx context.Context) {
	ids, err := s.store.SettingsUsers(ctx)
	if err != nil {
		return
	}
	for _, uid := range ids {
		st, _ := s.store.GetSettings(ctx, uid)
		if n, err := s.store.Tidy(ctx, uid, st.KeepDays); err != nil {
			s.log.Warn("tidying events failed", "user", uid, "err", err)
		} else if n > 0 {
			s.log.Debug("old events tidied away", "user", uid, "events", n)
		}
	}
}

// ---- Pocket Core ----

// coreLoop registers with Core on start (retrying until it answers) and
// re-registers every 30 minutes so version and last-seen stay fresh; each
// time it also reports how everybody connects (modes.go).
func (s *Server) coreLoop(ctx context.Context) {
	wait := 5 * time.Second
	for {
		err := s.registerWithCore(ctx)
		s.coreMu.Lock()
		s.coreOK, s.coreErr = err == nil, ""
		if err != nil {
			s.coreErr = err.Error()
		}
		s.coreMu.Unlock()
		if err != nil {
			s.log.Warn("pocket core: registration failed, retrying", "err", err, "in", wait.String())
		} else {
			wait = 30 * time.Minute
			s.reportModes(ctx)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		if err != nil && wait < 5*time.Minute {
			wait *= 2
		}
	}
}

// eventTopics are what Errors publishes on the Core bus.
var eventTopics = []string{"errors.issue_new", "errors.issue_regressed", "errors.issue_spike", "errors.issue_resolved"}

func (s *Server) registerWithCore(ctx context.Context) error {
	m := core.Manifest{
		Slug: "errors", Name: "Pocket Errors",
		Description: "Catches the errors of your apps and websites and groups them into issues: what broke, where in the code, how often, for how many people and since which release. Any Sentry SDK can send to it (browser, Go, Python, Node), Pocket apps through a Pocket Core link, and it can read this box's containers' logs for errors and panics with nothing to install. Resolve, ignore or come back to issues; new and returning errors reach you through Pocket Alerts; turn one into a Pocket Projects issue or a Pocket Tasks task; Pocket AI explains what went wrong and how to fix it.",
		Icon:        "/favicon.svg", Version: Version,
		URL: s.cfg.CoreAppURL, PublicURL: s.cfg.BaseURL, HealthPath: "/healthz", EnvPrefix: "ERRORS",
		Publishes: eventTopics,
		Offers:    linkOffers,
		Uses:      append(linkUses(), core.InferenceUse()),
	}
	if s.cfg.BaseURL != "" {
		m.OIDC = &core.OIDCRequest{RedirectURIs: []string{s.oidcCallbackURL()}}
	}
	reg, err := s.core.Register(ctx, s.cfg.CoreRegistrationKey, m)
	if err != nil {
		return err
	}
	if reg.Token != "" {
		s.log.Info("pocket core: registered, app token stored", "core", s.cfg.CoreURL)
	} else {
		s.log.Debug("pocket core: re-registered", "core", s.cfg.CoreURL)
	}
	return nil
}

// resolveSibling asks Core where a sibling app lives (cached a minute).
func (s *Server) resolveSibling(ctx context.Context, slug string) string {
	return s.resolve(ctx, slug).url
}

// resolvePublic is the address a person opens a sibling app at.
func (s *Server) resolvePublic(ctx context.Context, slug string) string {
	r := s.resolve(ctx, slug)
	return firstNonEmpty(r.public, r.url)
}

func (s *Server) resolve(ctx context.Context, slug string) resolvedURL {
	if !s.core.Configured() {
		return resolvedURL{}
	}
	s.coreMu.Lock()
	if r, ok := s.resolved[slug]; ok && time.Since(r.at) < time.Minute {
		s.coreMu.Unlock()
		return r
	}
	s.coreMu.Unlock()
	r := resolvedURL{at: time.Now()}
	if res, err := s.core.Resolve(ctx, slug); err == nil {
		r.url, r.public = res.URL, res.PublicURL
	}
	s.coreMu.Lock()
	s.resolved[slug] = r
	s.coreMu.Unlock()
	return r
}

// siblingLink is a page in a person's copy of a sibling app (their own
// public URL setting, else what Core knows), "" when unknown.
func (s *Server) siblingLink(ctx context.Context, uid int64, a pocketApp, path string) string {
	st, _ := s.store.GetSettings(ctx, uid)
	_, base, _, public := connSettings(st, a)
	base = firstNonEmpty(public, s.resolvePublic(ctx, a.slug), base)
	if base == "" {
		return ""
	}
	return strings.TrimRight(base, "/") + path
}

// coreStatus summarises the Pocket Core link for the About page.
func (s *Server) coreStatus() map[string]any {
	s.coreMu.Lock()
	defer s.coreMu.Unlock()
	return map[string]any{"configured": s.core.Configured(), "url": s.cfg.CoreURL, "registered": s.coreOK, "error": s.coreErr}
}

// publish sends an event for a person (fire and forget, through the outbox).
func (s *Server) publish(ctx context.Context, uid int64, topic, subject string, payload map[string]any) {
	if !s.core.Configured() {
		return
	}
	if payload == nil {
		payload = map[string]any{}
	}
	if u, err := s.store.GetUser(ctx, uid); err == nil {
		payload["user_email"] = u.Email
	}
	if err := s.core.Publish(ctx, topic, subject, payload); err != nil {
		s.log.Debug("pocket core: publish failed", "topic", topic, "err", err)
	}
}

package server

import (
	"context"
	"errors"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/abjelosevic88/pocket-homelab-errors/internal/intake"
	"github.com/abjelosevic88/pocket-homelab-errors/internal/store"
)

// ---- this box's containers ----
//
// An admin can have Errors read the logs of the containers on this box
// (Settings → This box; the Docker socket has to be mounted). Every 15
// seconds it reads what each matching container wrote since last time and
// turns error lines, Go panics and Python tracebacks into events — so every
// Pocket app's server errors arrive without changing anything in them. A
// container gets its project on its first error, named as Pocket Core knows
// the app ("pocket-notes" → "Pocket Notes"); archiving that project stops
// reading it. Old logs are not read: only what comes after it is switched on.

const watchEvery = 10 * time.Second

type watchState struct {
	On         bool               `json:"on"`
	Checked    string             `json:"checked_at"`
	Error      string             `json:"error"`
	Containers []watchedContainer `json:"containers"`

	readers map[string]*intake.LogReader
	since   map[string]time.Time
}

type watchedContainer struct {
	Name      string `json:"name"`
	Image     string `json:"image"`
	ProjectID int64  `json:"project_id"`
	Archived  bool   `json:"archived"`
	Errors    int    `json:"errors"` // since Errors started
	LastAt    string `json:"last_error_at"`
}

func (s *Server) watchLoop(ctx context.Context) {
	t := time.NewTicker(watchEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		s.newTick()
		s.collectLogs(ctx)
		ids, err := s.store.SettingsUsers(ctx)
		if err != nil {
			continue
		}
		for _, uid := range ids {
			st, _ := s.store.GetSettings(ctx, uid)
			u, err := s.store.GetUser(ctx, uid)
			if err != nil || !st.WatchDocker || u.Role != "admin" {
				s.watchMu.Lock()
				if w := s.watch[uid]; w != nil {
					w.On = false
				}
				s.watchMu.Unlock()
				continue
			}
			s.watchOnce(ctx, uid, st)
		}
	}
}

func (s *Server) watchFor(uid int64) *watchState {
	s.watchMu.Lock()
	defer s.watchMu.Unlock()
	w := s.watch[uid]
	if w == nil {
		w = &watchState{readers: map[string]*intake.LogReader{}, since: map[string]time.Time{}, Containers: []watchedContainer{}}
		s.watch[uid] = w
	}
	return w
}

// watchStatus is what the person sees of it (a copy).
func (s *Server) watchStatus(uid int64) watchState {
	s.watchMu.Lock()
	defer s.watchMu.Unlock()
	w := s.watch[uid]
	if w == nil {
		return watchState{Containers: []watchedContainer{}}
	}
	out := *w
	out.Containers = append([]watchedContainer{}, w.Containers...)
	return out
}

// self is this container's own id (Docker sets the hostname to it), so
// Errors never reads its own logs.
var self, _ = os.Hostname()

var rePocketContainer = regexp.MustCompile(`^pocket-([a-z0-9-]+?)(?:-app|-server|-web)?(?:-\d+)?$`)

func (s *Server) watchOnce(ctx context.Context, uid int64, st store.Settings) {
	w := s.watchFor(uid)
	fail := func(msg string) {
		s.watchMu.Lock()
		w.On, w.Error, w.Checked = true, msg, store.Now()
		s.watchMu.Unlock()
	}
	if !s.docker.available() {
		fail("The Docker socket is not here: mount /var/run/docker.sock into the container (read-only is enough)")
		return
	}
	cs, err := s.tickContainers(ctx)
	if err != nil {
		fail("Docker did not answer: " + err.Error())
		return
	}
	seen := []watchedContainer{}
	prev := map[string]watchedContainer{}
	s.watchMu.Lock()
	for _, c := range w.Containers {
		prev[c.Name] = c
	}
	s.watchMu.Unlock()
	for _, c := range cs {
		if !store.MatchName(st.WatchPattern, c.Name) || c.Name == "pocket-errors" || self != "" && strings.HasPrefix(c.ID, self) {
			continue
		}
		wc := prev[c.Name]
		wc.Name, wc.Image = c.Name, c.Image
		p, perr := s.store.ProjectByContainer(ctx, uid, c.Name)
		if perr == nil {
			wc.ProjectID, wc.Archived = p.ID, p.Archived
			if p.Archived {
				seen = append(seen, wc)
				continue
			}
		}
		s.readContainer(ctx, uid, w, c, p, &wc)
		seen = append(seen, wc)
	}
	sort.Slice(seen, func(i, j int) bool { return seen[i].Name < seen[j].Name })
	s.watchMu.Lock()
	w.On, w.Error, w.Checked, w.Containers = true, "", store.Now(), seen
	s.watchMu.Unlock()
}

// readContainer reads one container's new lines and files their errors.
func (s *Server) readContainer(ctx context.Context, uid int64, w *watchState, c container, p *store.Project, wc *watchedContainer) {
	key := "watch:" + itoa(uid) + ":" + c.Name
	s.watchMu.Lock()
	since, ok := w.since[c.Name]
	rd := w.readers[c.Name]
	if rd == nil {
		rd = &intake.LogReader{Container: c.Name}
		w.readers[c.Name] = rd
	}
	s.watchMu.Unlock()
	if !ok {
		if v, _, found := s.store.CacheGet(ctx, key); found {
			since, _ = time.Parse(time.RFC3339Nano, v)
		}
		if since.IsZero() {
			since = time.Now()
		}
	}
	if time.Since(since) > time.Hour {
		since = time.Now().Add(-time.Hour)
	}
	rd.Warnings = p != nil && p.MinLevel == "warning"
	lines, err := s.tickLogs(ctx, c.ID, since)
	if err != nil {
		s.log.Debug("reading a container's logs failed", "container", c.Name, "err", err)
		return
	}
	file := func(ev *intake.Event) {
		if ev == nil {
			return
		}
		if err := s.fileContainerEvent(ctx, uid, c, ev); err != nil {
			s.log.Debug("keeping a container error failed", "container", c.Name, "err", err)
			return
		}
		wc.Errors++
		wc.LastAt = store.Stamp(ev.At)
		if pp, err := s.store.ProjectByContainer(ctx, uid, c.Name); err == nil {
			wc.ProjectID = pp.ID
		}
	}
	last := since
	for _, l := range lines {
		if !l.at.After(since) {
			continue
		}
		for _, ev := range rd.Line(l.at, l.text) {
			file(ev)
		}
		last = l.at
	}
	if len(lines) == 0 || last.Equal(since) {
		file(rd.Flush())
	}
	s.watchMu.Lock()
	w.since[c.Name] = last
	s.watchMu.Unlock()
	if !last.Equal(since) || !ok {
		_ = s.store.CachePut(ctx, key, last.UTC().Format(time.RFC3339Nano))
	}
}

// fileContainerEvent files an error from a container's logs under its
// project, making the project on the first one.
func (s *Server) fileContainerEvent(ctx context.Context, uid int64, c container, ev *intake.Event) error {
	p, err := s.store.ProjectByContainer(ctx, uid, c.Name)
	if errors.Is(err, store.ErrNotFound) {
		slug := ""
		name := c.Name
		if m := rePocketContainer.FindStringSubmatch(c.Name); m != nil {
			slug = m[1]
			name = s.appName(ctx, slug)
		}
		p = &store.Project{UserID: uid, Name: name, Platform: "docker", Source: "docker", Container: c.Name, AppSlug: slug, NotifyNew: true, NotifyAgain: true}
		if err = s.store.CreateProject(ctx, p); errors.Is(err, store.ErrConflict) {
			p, err = s.store.ProjectByContainer(ctx, uid, c.Name)
		}
	}
	if err != nil {
		return err
	}
	if p.Archived {
		return nil
	}
	ev.Data.ServerName = c.Name
	ev.Data.Tags = append([][2]string{{"container", c.Name}, {"image", c.Image}}, ev.Data.Tags...)
	if i := strings.LastIndex(c.Image, ":"); i > 0 && !strings.Contains(c.Image[i:], "/") && ev.Release == "" {
		ev.Release = strings.TrimPrefix(c.Image[i+1:], "v")
		if ev.Release == "latest" || ev.Release == "local" {
			ev.Release = ""
		}
	}
	_, err = s.ingest(ctx, p, ev)
	return err
}

// handleWatch is the containers being read, for Settings and Projects.
func (s *Server) handleWatch(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	uid := actorID(ctx)
	st, _ := s.store.GetSettings(ctx, uid)
	u := userFrom(ctx)
	out := map[string]any{"available": s.docker.available(), "admin": u.Role == "admin", "on": st.WatchDocker, "pattern": st.WatchPattern,
		"status": s.watchStatus(uid)}
	if u.Role == "admin" && s.docker.available() && r.URL.Query().Get("all") == "1" {
		cs, err := s.docker.containers(ctx)
		if err == nil {
			names := []map[string]any{}
			for _, c := range cs {
				names = append(names, map[string]any{"name": c.Name, "image": c.Image, "match": store.MatchName(st.WatchPattern, c.Name) && c.Name != "pocket-errors"})
			}
			sort.Slice(names, func(i, j int) bool { return names[i]["name"].(string) < names[j]["name"].(string) })
			out["containers"] = names
		} else {
			out["error"] = err.Error()
		}
	}
	writeJSON(w, http.StatusOK, out)
}

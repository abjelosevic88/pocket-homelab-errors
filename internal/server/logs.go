package server

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/abjelosevic88/pocket-homelab-errors/internal/logs"
	"github.com/abjelosevic88/pocket-homelab-errors/internal/store"
)

// ---- every line this box writes ----
//
// Beside the errors, an admin can have Errors keep every line: each
// container's (the same read the error watcher does, so Docker is asked
// once) and the systemd journal's (system and user services, the kernel),
// in logs.db with full-text search, counts that outlive the lines, the
// messages that repeat and the web traffic Caddy logs. Box-wide: one admin
// switches it on for the box, only admins see the lines.

type logSettings struct {
	On       bool   `json:"on"`
	Pattern  string `json:"pattern"` // containers, * allowed, comma separated
	Journal  bool   `json:"journal"`
	KeepDays int    `json:"keep_days"`
	MaxMB    int    `json:"max_mb"`
}

func defaultLogSettings() logSettings {
	return logSettings{Pattern: "*", Journal: true, KeepDays: 7, MaxMB: 2048}
}

func (s *Server) logSettings(ctx context.Context) logSettings {
	st := defaultLogSettings()
	if s.logs == nil {
		return st
	}
	if v := s.logs.Meta(ctx, "settings"); v != "" {
		_ = json.Unmarshal([]byte(v), &st)
	}
	if st.Pattern == "" {
		st.Pattern = "*"
	}
	if st.KeepDays < 1 {
		st.KeepDays = 7
	}
	return st
}

func (s *Server) saveLogSettings(ctx context.Context, st logSettings) error {
	b, _ := json.Marshal(st)
	return s.logs.SetMeta(ctx, "settings", string(b))
}

// logCollector is the state of reading containers for the logs.
type logCollector struct {
	mu      sync.Mutex
	pos     map[string]time.Time // container → the last line kept
	prev    map[string]int       // container → the level of its last line
	loaded  bool
	checked time.Time
	read    int // containers read last time
	lines   int64
	err     string
	dirty   bool
}

// tickReads shares one Docker read per container per tick between the
// logs and the error watcher.
type tickReads struct {
	mu    sync.Mutex
	cs    []container
	csErr error
	csOK  bool
	reads map[string]*tickRead
}

type tickRead struct {
	since time.Time
	lines []logLine
	err   error
}

func (s *Server) newTick() {
	s.tickMu.Lock()
	s.tick = &tickReads{reads: map[string]*tickRead{}}
	s.tickMu.Unlock()
}

func (s *Server) curTick() *tickReads {
	s.tickMu.Lock()
	defer s.tickMu.Unlock()
	if s.tick == nil {
		s.tick = &tickReads{reads: map[string]*tickRead{}}
	}
	return s.tick
}

// tickContainers lists the containers once a tick.
func (s *Server) tickContainers(ctx context.Context) ([]container, error) {
	t := s.curTick()
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.csOK {
		t.cs, t.csErr = s.docker.containers(ctx)
		t.csOK = true
	}
	return t.cs, t.csErr
}

// tickLogs reads a container's lines after since, from Docker or from what
// this tick already read.
func (s *Server) tickLogs(ctx context.Context, id string, since time.Time) ([]logLine, error) {
	t := s.curTick()
	t.mu.Lock()
	r := t.reads[id]
	t.mu.Unlock()
	if r != nil && !r.since.After(since) {
		if r.err != nil {
			return nil, r.err
		}
		out := make([]logLine, 0, len(r.lines))
		for _, l := range r.lines {
			if l.at.After(since) {
				out = append(out, l)
			}
		}
		return out, nil
	}
	lines, err := s.docker.logs(ctx, id, sinceArg(since), 4<<20)
	t.mu.Lock()
	t.reads[id] = &tickRead{since: since, lines: lines, err: err}
	t.mu.Unlock()
	return lines, err
}

// collectLogs keeps the new lines of every matching container.
func (s *Server) collectLogs(ctx context.Context) {
	if s.logs == nil {
		return
	}
	st := s.logSettings(ctx)
	lc := s.logColl
	fail := func(msg string) {
		lc.mu.Lock()
		lc.err, lc.checked = msg, time.Now()
		lc.mu.Unlock()
	}
	if !st.On {
		fail("")
		return
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
	lc.mu.Lock()
	if !lc.loaded {
		if v := s.logs.Meta(ctx, "docker_pos"); v != "" {
			var m map[string]string
			if json.Unmarshal([]byte(v), &m) == nil {
				for k, x := range m {
					if t, err := time.Parse(time.RFC3339Nano, x); err == nil {
						lc.pos[k] = t
					}
				}
			}
		}
		lc.loaded = true
	}
	lc.mu.Unlock()
	var batch []logs.Entry
	read := 0
	for _, c := range cs {
		if !store.MatchName(st.Pattern, c.Name) || c.Name == "pocket-errors" || self != "" && strings.HasPrefix(c.ID, self) {
			continue
		}
		si := s.containerSource(ctx, c)
		src, err := s.logs.Ensure(ctx, si)
		if err != nil {
			continue
		}
		key := logs.SourceKey(src.Kind, src.Name)
		lc.mu.Lock()
		since, ok := lc.pos[c.Name]
		prev := lc.prev[c.Name]
		lc.mu.Unlock()
		if !ok || since.IsZero() {
			since = time.Now().Add(-30 * time.Second)
		}
		if time.Since(since) > time.Hour {
			since = time.Now().Add(-time.Hour)
		}
		lines, err := s.tickLogs(ctx, c.ID, since)
		if err != nil {
			continue
		}
		read++
		last := since
		for _, l := range lines {
			if !l.at.After(since) {
				continue
			}
			e := logs.Parse(l.text, prev)
			e.At, e.Source = l.at, key
			prev = e.Level
			batch = append(batch, e)
			last = l.at
		}
		lc.mu.Lock()
		if !last.Equal(since) || !ok {
			lc.pos[c.Name] = last
			lc.dirty = true
		}
		lc.prev[c.Name] = prev
		lc.mu.Unlock()
	}
	n, err := s.logs.Write(ctx, batch)
	lc.mu.Lock()
	lc.checked, lc.read, lc.err = time.Now(), read, ""
	lc.lines += int64(len(batch))
	if err != nil {
		lc.err = "Keeping the lines failed: " + err.Error()
	}
	var save map[string]string
	if lc.dirty && err == nil {
		save = map[string]string{}
		for k, t := range lc.pos {
			save[k] = t.UTC().Format(time.RFC3339Nano)
		}
		lc.dirty = false
	}
	lc.mu.Unlock()
	if save != nil {
		b, _ := json.Marshal(save)
		_ = s.logs.SetMeta(ctx, "docker_pos", string(b))
	}
	if err != nil {
		s.log.Warn("keeping log lines failed", "lines", len(batch), "err", err)
	} else if n > 0 {
		s.log.Debug("log lines kept", "lines", n)
	}
}

// containerSource names a container as a source: Pocket apps by their
// name in Pocket Core.
func (s *Server) containerSource(ctx context.Context, c container) logs.SourceInfo {
	si := logs.SourceInfo{Kind: "docker", Name: c.Name, Label: c.Name, Group: "docker", Image: c.Image}
	if m := rePocketContainer.FindStringSubmatch(c.Name); m != nil {
		si.Group = "pocket"
		if known := s.logs.SourceByName(c.Name); known != nil && known.Label != "" && known.Label != c.Name {
			si.Label = known.Label
		} else {
			si.Label = s.appName(ctx, m[1])
		}
	}
	return si
}

// logsLoop follows the journal while it is switched on and tidies the
// logs every quarter of an hour.
func (s *Server) logsLoop(ctx context.Context) {
	if s.logs == nil {
		return
	}
	var stop context.CancelFunc
	tidy := time.NewTicker(15 * time.Minute)
	defer tidy.Stop()
	check := time.NewTicker(20 * time.Second)
	defer check.Stop()
	s.tidyLogs(ctx)
	for {
		st := s.logSettings(ctx)
		want := st.On && st.Journal && s.journal.Available()
		if want && stop == nil {
			jctx, cancel := context.WithCancel(ctx)
			stop = cancel
			go s.journal.Run(jctx, s.logs.Meta(ctx, "journal_cursor"), s.keepJournal, func(c string) {
				_ = s.logs.SetMeta(ctx, "journal_cursor", c)
			})
		} else if !want && stop != nil {
			stop()
			stop = nil
		}
		select {
		case <-ctx.Done():
			if stop != nil {
				stop()
			}
			return
		case <-check.C:
		case <-tidy.C:
			s.tidyLogs(ctx)
		}
	}
}

func (s *Server) tidyLogs(ctx context.Context) {
	st := s.logSettings(ctx)
	if n, err := s.logs.Tidy(ctx, st.KeepDays, st.MaxMB); err != nil {
		s.log.Warn("tidying log lines failed", "err", err)
	} else if n > 0 {
		s.log.Debug("old log lines tidied away", "lines", n)
	}
}

// keepJournal keeps a batch of journal records.
func (s *Server) keepJournal(recs []logs.JournalEntry) {
	ctx := context.Background()
	batch := make([]logs.Entry, 0, len(recs))
	prev := map[string]int{}
	for _, r := range recs {
		si := r.Source()
		src, err := s.logs.Ensure(ctx, si)
		if err != nil {
			continue
		}
		key := logs.SourceKey(src.Kind, src.Name)
		e := r.Entry()
		if e.Level == logs.Info && prev[key] >= logs.Warn && (strings.HasPrefix(r.Message, " ") || strings.HasPrefix(r.Message, "\t")) {
			e.Level = prev[key]
		}
		prev[key] = e.Level
		e.Source = key
		batch = append(batch, e)
	}
	if _, err := s.logs.Write(ctx, batch); err != nil {
		s.log.Warn("keeping journal lines failed", "lines", len(batch), "err", err)
	}
}

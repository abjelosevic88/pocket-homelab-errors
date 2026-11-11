package logs

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// ErrNotFound is returned for a line or source that is not there.
var ErrNotFound = errors.New("not found")

// DB is the logs database.
type DB struct {
	db  *sql.DB
	log *slog.Logger

	mu      sync.Mutex
	sources map[string]*Source // by kind/name
	byID    map[int64]*Source
	writeMu sync.Mutex
}

// Source is one container or service that writes lines.
type Source struct {
	ID      int64  `json:"id"`
	Kind    string `json:"kind"`  // docker | journal
	Name    string `json:"name"`  // the container, or the unit ("user:pocket-ai.service", "kernel")
	Label   string `json:"label"` // how it is shown
	Group   string `json:"group"` // pocket | docker | system | user | kernel
	Image   string `json:"image,omitempty"`
	Keep    string `json:"keep"` // all | warn | error | count
	FirstAt int64  `json:"first_at"`
	LastAt  int64  `json:"last_at"`
}

// KeepModes are what a source's lines may be kept as.
var KeepModes = []string{"all", "warn", "error", "count"}

func keepMin(k string) int {
	switch k {
	case "warn":
		return Warn
	case "error":
		return Error
	case "count":
		return 99
	}
	return 0
}

const schema = `
CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS sources (
  id INTEGER PRIMARY KEY, kind TEXT NOT NULL, name TEXT NOT NULL, label TEXT NOT NULL, grp TEXT NOT NULL,
  image TEXT NOT NULL DEFAULT '', keep TEXT NOT NULL DEFAULT 'all', first_at INTEGER NOT NULL, last_at INTEGER NOT NULL,
  UNIQUE(kind, name));
CREATE TABLE IF NOT EXISTS lines (
  id INTEGER PRIMARY KEY, at INTEGER NOT NULL, source_id INTEGER NOT NULL, level INTEGER NOT NULL,
  stream INTEGER NOT NULL DEFAULT 0, pattern INTEGER NOT NULL DEFAULT 0, msg TEXT NOT NULL, attrs TEXT);
CREATE INDEX IF NOT EXISTS lines_at ON lines(at);
CREATE INDEX IF NOT EXISTS lines_src_at ON lines(source_id, at);
CREATE INDEX IF NOT EXISTS lines_bad ON lines(level, at) WHERE level >= 3;
CREATE INDEX IF NOT EXISTS lines_pattern ON lines(pattern, at);
CREATE VIRTUAL TABLE IF NOT EXISTS lines_fts USING fts5(body, content='', contentless_delete=1, tokenize='unicode61');
CREATE TABLE IF NOT EXISTS roll5 (b INTEGER NOT NULL, source_id INTEGER NOT NULL, level INTEGER NOT NULL, n INTEGER NOT NULL, bytes INTEGER NOT NULL,
  PRIMARY KEY (b, source_id, level)) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS roll60 (b INTEGER NOT NULL, source_id INTEGER NOT NULL, level INTEGER NOT NULL, n INTEGER NOT NULL, bytes INTEGER NOT NULL,
  PRIMARY KEY (b, source_id, level)) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS web5 (b INTEGER NOT NULL, host TEXT NOT NULL, class INTEGER NOT NULL, n INTEGER NOT NULL, ms REAL NOT NULL, slow INTEGER NOT NULL,
  PRIMARY KEY (b, host, class)) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS web60 (b INTEGER NOT NULL, host TEXT NOT NULL, class INTEGER NOT NULL, n INTEGER NOT NULL, ms REAL NOT NULL, slow INTEGER NOT NULL,
  PRIMARY KEY (b, host, class)) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS patterns (
  id INTEGER PRIMARY KEY, source_id INTEGER NOT NULL, level INTEGER NOT NULL, template TEXT NOT NULL, sample TEXT NOT NULL,
  n INTEGER NOT NULL, first_at INTEGER NOT NULL, last_at INTEGER NOT NULL);
CREATE INDEX IF NOT EXISTS patterns_last ON patterns(last_at);
CREATE TABLE IF NOT EXISTS pattern60 (pattern INTEGER NOT NULL, b INTEGER NOT NULL, n INTEGER NOT NULL, PRIMARY KEY (pattern, b)) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS pattern60_b ON pattern60(b);
`

// Open opens (and makes) the logs database.
func Open(path string, log *slog.Logger) (*DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	_, statErr := os.Stat(path)
	fresh := os.IsNotExist(statErr)
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)&_pragma=synchronous(NORMAL)&_pragma=temp_store(MEMORY)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	db.SetConnMaxLifetime(0)
	if fresh {
		// space from deleted lines can be handed back to the disk
		if _, err := db.Exec(`PRAGMA auto_vacuum = INCREMENTAL`); err != nil {
			return nil, err
		}
	}
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("logs schema: %w", err)
	}
	d := &DB{db: db, log: log, sources: map[string]*Source{}, byID: map[int64]*Source{}}
	if err := d.loadSources(context.Background()); err != nil {
		return nil, err
	}
	return d, nil
}

// Close closes it.
func (d *DB) Close() error { return d.db.Close() }

// SQL is the handle (tests, backups).
func (d *DB) SQL() *sql.DB { return d.db }

func (d *DB) loadSources(ctx context.Context) error {
	rows, err := d.db.QueryContext(ctx, `SELECT id, kind, name, label, grp, image, keep, first_at, last_at FROM sources`)
	if err != nil {
		return err
	}
	defer rows.Close()
	d.mu.Lock()
	defer d.mu.Unlock()
	for rows.Next() {
		s := &Source{}
		if err := rows.Scan(&s.ID, &s.Kind, &s.Name, &s.Label, &s.Group, &s.Image, &s.Keep, &s.FirstAt, &s.LastAt); err != nil {
			return err
		}
		d.sources[s.Kind+"/"+s.Name] = s
		d.byID[s.ID] = s
	}
	return rows.Err()
}

// SourceKey names a source for Entry.Source.
func SourceKey(kind, name string) string { return kind + "/" + name }

// SourceInfo is what the collector knows of a source when it first writes.
type SourceInfo struct {
	Kind, Name, Label, Group, Image string
}

// Ensure makes a source known (or refreshes its label and image).
func (d *DB) Ensure(ctx context.Context, si SourceInfo) (*Source, error) {
	key := si.Kind + "/" + si.Name
	d.mu.Lock()
	s := d.sources[key]
	d.mu.Unlock()
	if s != nil {
		if (si.Label != "" && si.Label != s.Label) || (si.Image != "" && si.Image != s.Image) {
			d.mu.Lock()
			if si.Label != "" {
				s.Label = si.Label
			}
			if si.Image != "" {
				s.Image = si.Image
			}
			d.mu.Unlock()
			_, _ = d.db.ExecContext(ctx, `UPDATE sources SET label = ?, image = ? WHERE id = ?`, s.Label, s.Image, s.ID)
		}
		return s, nil
	}
	now := time.Now().UnixMilli()
	if si.Label == "" {
		si.Label = si.Name
	}
	res, err := d.db.ExecContext(ctx, `INSERT INTO sources (kind, name, label, grp, image, keep, first_at, last_at) VALUES (?, ?, ?, ?, ?, 'all', ?, ?)
		ON CONFLICT(kind, name) DO NOTHING`, si.Kind, si.Name, si.Label, si.Group, si.Image, now, now)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if err := d.loadSources(ctx); err != nil {
			return nil, err
		}
		d.mu.Lock()
		defer d.mu.Unlock()
		if s = d.sources[key]; s == nil {
			return nil, ErrNotFound
		}
		return s, nil
	}
	id, _ := res.LastInsertId()
	s = &Source{ID: id, Kind: si.Kind, Name: si.Name, Label: si.Label, Group: si.Group, Image: si.Image, Keep: "all", FirstAt: now, LastAt: now}
	d.mu.Lock()
	d.sources[key], d.byID[id] = s, s
	d.mu.Unlock()
	return s, nil
}

// SourceByID returns a known source.
func (d *DB) SourceByID(id int64) *Source {
	d.mu.Lock()
	defer d.mu.Unlock()
	if s := d.byID[id]; s != nil {
		c := *s
		return &c
	}
	return nil
}

// SourceByName finds a source by its name (any kind).
func (d *DB) SourceByName(name string) *Source {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, s := range d.byID {
		if s.Name == name {
			c := *s
			return &c
		}
	}
	return nil
}

// AllSources lists the known sources.
func (d *DB) AllSources() []Source {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]Source, 0, len(d.byID))
	for _, s := range d.byID {
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Label < out[j].Label })
	return out
}

// SetKeep changes what is kept of a source's lines.
func (d *DB) SetKeep(ctx context.Context, id int64, keep string) (*Source, error) {
	ok := false
	for _, k := range KeepModes {
		ok = ok || k == keep
	}
	if !ok {
		return nil, fmt.Errorf("keep is all, warn, error or count")
	}
	d.mu.Lock()
	s := d.byID[id]
	if s != nil {
		s.Keep = keep
	}
	d.mu.Unlock()
	if s == nil {
		return nil, ErrNotFound
	}
	_, err := d.db.ExecContext(ctx, `UPDATE sources SET keep = ? WHERE id = ?`, keep, id)
	c := *s
	return &c, err
}

// ---- writing ----

type rollKey struct {
	b   int64
	src int64
	lv  int
}

type webKey struct {
	b     int64
	host  string
	class int
}

type webVal struct {
	n    int
	ms   float64
	slow int
}

type patVal struct {
	src      int64
	lv       int
	template string
	sample   string
	n        int
	first    int64
	last     int64
	hours    map[int64]int
}

// Write keeps a batch of lines (each with Source set to a key made known
// with Ensure) and counts them, all in one transaction.
func (d *DB) Write(ctx context.Context, entries []Entry) (int, error) {
	if len(entries) == 0 {
		return 0, nil
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].At.Before(entries[j].At) })
	d.writeMu.Lock()
	defer d.writeMu.Unlock()
	r5 := map[rollKey][2]int{}
	r60 := map[rollKey][2]int{}
	w5 := map[webKey]webVal{}
	w60 := map[webKey]webVal{}
	pats := map[int64]*patVal{}
	lastAt := map[int64]int64{}

	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	insLine, err := tx.PrepareContext(ctx, `INSERT INTO lines (at, source_id, level, stream, pattern, msg, attrs) VALUES (?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return 0, err
	}
	defer insLine.Close()
	insFTS, err := tx.PrepareContext(ctx, `INSERT INTO lines_fts (rowid, body) VALUES (?, ?)`)
	if err != nil {
		return 0, err
	}
	defer insFTS.Close()
	kept := 0
	for i := range entries {
		e := &entries[i]
		d.mu.Lock()
		s := d.sources[e.Source]
		var src int64
		keep := "all"
		if s != nil {
			src, keep = s.ID, s.Keep
		}
		d.mu.Unlock()
		if src == 0 {
			continue
		}
		ms := e.At.UnixMilli()
		sec := e.At.Unix()
		b5, b60 := sec-sec%300, sec-sec%3600
		add := func(m map[rollKey][2]int, k rollKey) {
			v := m[k]
			v[0]++
			v[1] += e.Bytes
			m[k] = v
		}
		add(r5, rollKey{b5, src, e.Level})
		add(r60, rollKey{b60, src, e.Level})
		if ms > lastAt[src] {
			lastAt[src] = ms
		}
		var tpl string
		if e.Web != nil && e.Web.Host != "" {
			class := e.Web.Status / 100
			for _, x := range []struct {
				m map[webKey]webVal
				b int64
			}{{w5, b5}, {w60, b60}} {
				k := webKey{x.b, e.Web.Host, class}
				v := x.m[k]
				v.n++
				v.ms += e.Web.Ms
				if e.Web.Ms >= 1000 {
					v.slow++
				}
				x.m[k] = v
			}
			tpl = webTemplate(e.Web)
		} else {
			tpl = Template(e.Msg)
		}
		pid := PatternID(src, tpl)
		p := pats[pid]
		if p == nil {
			p = &patVal{src: src, lv: e.Level, template: tpl, sample: clip(e.Msg, 500), first: ms, hours: map[int64]int{}}
			pats[pid] = p
		}
		p.n++
		p.last = ms
		p.lv = max(p.lv, e.Level)
		p.hours[b60]++
		if e.Level < keepMin(keep) {
			continue
		}
		var attrs any
		body := e.Msg
		if len(e.Attrs) > 0 {
			b, _ := json.Marshal(e.Attrs)
			if len(b) > 4000 {
				b = b[:0]
				b, _ = json.Marshal(map[string]string{"_clipped": "the fields were too long to keep"})
			}
			attrs = string(b)
			for _, k := range sortedKeys(e.Attrs) {
				if k == "ua" || k == "proto" {
					continue
				}
				body += " " + e.Attrs[k]
			}
		}
		res, err := insLine.ExecContext(ctx, ms, src, e.Level, e.Stream, pid, e.Msg, attrs)
		if err != nil {
			return kept, err
		}
		id, _ := res.LastInsertId()
		if _, err := insFTS.ExecContext(ctx, id, body); err != nil {
			return kept, err
		}
		kept++
	}
	upRoll := func(table string, m map[rollKey][2]int) error {
		st, err := tx.PrepareContext(ctx, `INSERT INTO `+table+` (b, source_id, level, n, bytes) VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(b, source_id, level) DO UPDATE SET n = n + excluded.n, bytes = bytes + excluded.bytes`)
		if err != nil {
			return err
		}
		defer st.Close()
		for k, v := range m {
			if _, err := st.ExecContext(ctx, k.b, k.src, k.lv, v[0], v[1]); err != nil {
				return err
			}
		}
		return nil
	}
	upWeb := func(table string, m map[webKey]webVal) error {
		st, err := tx.PrepareContext(ctx, `INSERT INTO `+table+` (b, host, class, n, ms, slow) VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT(b, host, class) DO UPDATE SET n = n + excluded.n, ms = ms + excluded.ms, slow = slow + excluded.slow`)
		if err != nil {
			return err
		}
		defer st.Close()
		for k, v := range m {
			if _, err := st.ExecContext(ctx, k.b, k.host, k.class, v.n, v.ms, v.slow); err != nil {
				return err
			}
		}
		return nil
	}
	if err := upRoll("roll5", r5); err != nil {
		return kept, err
	}
	if err := upRoll("roll60", r60); err != nil {
		return kept, err
	}
	if err := upWeb("web5", w5); err != nil {
		return kept, err
	}
	if err := upWeb("web60", w60); err != nil {
		return kept, err
	}
	pst, err := tx.PrepareContext(ctx, `INSERT INTO patterns (id, source_id, level, template, sample, n, first_at, last_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET n = n + excluded.n, last_at = max(last_at, excluded.last_at), level = max(level, excluded.level), sample = excluded.sample`)
	if err != nil {
		return kept, err
	}
	defer pst.Close()
	hst, err := tx.PrepareContext(ctx, `INSERT INTO pattern60 (pattern, b, n) VALUES (?, ?, ?) ON CONFLICT(pattern, b) DO UPDATE SET n = n + excluded.n`)
	if err != nil {
		return kept, err
	}
	defer hst.Close()
	for id, p := range pats {
		if _, err := pst.ExecContext(ctx, id, p.src, p.lv, p.template, p.sample, p.n, p.first, p.last); err != nil {
			return kept, err
		}
		for b, n := range p.hours {
			if _, err := hst.ExecContext(ctx, id, b, n); err != nil {
				return kept, err
			}
		}
	}
	for src, at := range lastAt {
		if _, err := tx.ExecContext(ctx, `UPDATE sources SET last_at = max(last_at, ?) WHERE id = ?`, at, src); err != nil {
			return kept, err
		}
	}
	if err := tx.Commit(); err != nil {
		return kept, err
	}
	d.mu.Lock()
	for src, at := range lastAt {
		if s := d.byID[src]; s != nil && at > s.LastAt {
			s.LastAt = at
		}
	}
	d.mu.Unlock()
	return kept, nil
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ---- meta ----

// Meta reads a value kept for the collectors (a journal cursor).
func (d *DB) Meta(ctx context.Context, key string) string {
	var v string
	_ = d.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`, key).Scan(&v)
	return v
}

// SetMeta keeps a value.
func (d *DB) SetMeta(ctx context.Context, key, value string) error {
	_, err := d.db.ExecContext(ctx, `INSERT INTO meta (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// ---- size and tidying ----

// Size is the bytes the database uses (and its file holds).
func (d *DB) Size(ctx context.Context) (used, file int64) {
	var pages, free, size int64
	_ = d.db.QueryRowContext(ctx, `PRAGMA page_count`).Scan(&pages)
	_ = d.db.QueryRowContext(ctx, `PRAGMA freelist_count`).Scan(&free)
	_ = d.db.QueryRowContext(ctx, `PRAGMA page_size`).Scan(&size)
	return (pages - free) * size, pages * size
}

// Counts are the lines kept and their span.
func (d *DB) Counts(ctx context.Context) (lines int64, oldest, newest int64) {
	_ = d.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM lines`).Scan(&lines)
	_ = d.db.QueryRowContext(ctx, `SELECT COALESCE(MIN(at),0), COALESCE(MAX(at),0) FROM lines`).Scan(&oldest, &newest)
	return
}

// Tidy drops lines older than keepDays, then the oldest until the
// database fits in maxMB, and counts older than 90 days (patterns not seen
// for 30). It returns the lines dropped.
func (d *DB) Tidy(ctx context.Context, keepDays, maxMB int) (int64, error) {
	d.writeMu.Lock()
	defer d.writeMu.Unlock()
	var dropped int64
	cut := time.Now().AddDate(0, 0, -keepDays).UnixMilli()
	// lines go oldest first in chunks, so the database never holds a long write
	for {
		var maxID int64
		if err := d.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(id), 0) FROM (SELECT id FROM lines WHERE at < ? ORDER BY at LIMIT 20000)`, cut).Scan(&maxID); err != nil || maxID == 0 {
			break
		}
		n, err := d.dropUpTo(ctx, maxID, cut)
		if err != nil {
			return dropped, err
		}
		dropped += n
		if n == 0 {
			break
		}
	}
	if maxMB > 0 {
		limit := int64(maxMB) << 20
		for i := 0; i < 200; i++ {
			used, _ := d.Size(ctx)
			if used <= limit {
				break
			}
			var lo, hi int64
			_ = d.db.QueryRowContext(ctx, `SELECT COALESCE(MIN(id),0), COALESCE(MAX(id),0) FROM lines`).Scan(&lo, &hi)
			if hi == 0 || lo >= hi {
				break
			}
			step := max((hi-lo)/50, 1000) // 2 % of what is kept at a time
			n, err := d.dropUpTo(ctx, lo+step, 0)
			if err != nil {
				return dropped, err
			}
			dropped += n
		}
	}
	old := time.Now().AddDate(0, 0, -90).Unix()
	for _, q := range []struct {
		sql string
		arg any
	}{
		{`DELETE FROM roll5 WHERE b < ?`, time.Now().Add(-72 * time.Hour).Unix()},
		{`DELETE FROM web5 WHERE b < ?`, time.Now().Add(-72 * time.Hour).Unix()},
		{`DELETE FROM roll60 WHERE b < ?`, old},
		{`DELETE FROM web60 WHERE b < ?`, old},
		{`DELETE FROM pattern60 WHERE b < ?`, time.Now().AddDate(0, 0, -31).Unix()},
		{`DELETE FROM patterns WHERE last_at < ?`, time.Now().AddDate(0, 0, -30).UnixMilli()},
	} {
		if _, err := d.db.ExecContext(ctx, q.sql, q.arg); err != nil {
			return dropped, err
		}
	}
	if dropped > 0 {
		_, _ = d.db.ExecContext(ctx, `PRAGMA incremental_vacuum`)
	}
	return dropped, nil
}

// dropUpTo deletes the lines with id <= maxID (and at < before when given).
func (d *DB) dropUpTo(ctx context.Context, maxID, before int64) (int64, error) {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	q := `SELECT id FROM lines WHERE id <= ?`
	args := []any{maxID}
	if before > 0 {
		q += ` AND at < ?`
		args = append(args, before)
	}
	rows, err := tx.QueryContext(ctx, q, args...)
	if err != nil {
		return 0, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	if len(ids) == 0 {
		return 0, nil
	}
	delF, err := tx.PrepareContext(ctx, `DELETE FROM lines_fts WHERE rowid = ?`)
	if err != nil {
		return 0, err
	}
	defer delF.Close()
	delL, err := tx.PrepareContext(ctx, `DELETE FROM lines WHERE id = ?`)
	if err != nil {
		return 0, err
	}
	defer delL.Close()
	for _, id := range ids {
		if _, err := delF.ExecContext(ctx, id); err != nil {
			return 0, err
		}
		if _, err := delL.ExecContext(ctx, id); err != nil {
			return 0, err
		}
	}
	return int64(len(ids)), tx.Commit()
}

// Clear drops every line and count (the sources and their keep modes stay).
func (d *DB) Clear(ctx context.Context) error {
	d.writeMu.Lock()
	defer d.writeMu.Unlock()
	for _, t := range []string{"lines", "roll5", "roll60", "web5", "web60", "patterns", "pattern60"} {
		if _, err := d.db.ExecContext(ctx, `DELETE FROM `+t); err != nil {
			return err
		}
	}
	if _, err := d.db.ExecContext(ctx, `DELETE FROM lines_fts`); err != nil {
		return err
	}
	_, _ = d.db.ExecContext(ctx, `PRAGMA incremental_vacuum`)
	return nil
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func placeholders(n int) string { return strings.TrimSuffix(strings.Repeat("?,", n), ",") }

package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"strings"
	"time"
)

// NewKey is a fresh project key (32 hex characters, like a Sentry DSN key).
func NewKey() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

const projectCols = `id, user_id, name, platform, public_key, source, container, app_slug, notify_new, notify_again, notify_spike,
	min_level, rate_limit, archived, last_event_at, created_at, updated_at`

func scanProject(sc interface{ Scan(...any) error }) (*Project, error) {
	p := &Project{}
	var last sql.NullString
	err := sc.Scan(&p.ID, &p.UserID, &p.Name, &p.Platform, &p.PublicKey, &p.Source, &p.Container, &p.AppSlug, &p.NotifyNew, &p.NotifyAgain,
		&p.NotifySpike, &p.MinLevel, &p.RateLimit, &p.Archived, &last, &p.CreatedAt, &p.UpdatedAt)
	if last.Valid {
		p.LastEventAt = &last.String
	}
	return p, err
}

// CreateProject adds a project for p.UserID (a new key when none is set).
func (s *Store) CreateProject(ctx context.Context, p *Project) error {
	if p.PublicKey == "" {
		p.PublicKey = NewKey()
	}
	if p.Platform == "" {
		p.Platform = "other"
	}
	if p.MinLevel == "" {
		p.MinLevel = "error"
	}
	now := Now()
	res, err := s.DB.ExecContext(ctx, `INSERT INTO projects (user_id, name, platform, public_key, source, container, app_slug, notify_new, notify_again,
		notify_spike, min_level, rate_limit, archived, created_at, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		p.UserID, p.Name, p.Platform, p.PublicKey, p.Source, p.Container, p.AppSlug, p.NotifyNew, p.NotifyAgain, p.NotifySpike, p.MinLevel,
		p.RateLimit, p.Archived, now, now)
	if err != nil {
		return conflict(err)
	}
	p.ID, _ = res.LastInsertId()
	p.CreatedAt, p.UpdatedAt = now, now
	return nil
}

// GetProject is one of the person's projects.
func (s *Store) GetProject(ctx context.Context, uid, id int64) (*Project, error) {
	return notFoundP(scanProject(s.DB.QueryRowContext(ctx, `SELECT `+projectCols+` FROM projects WHERE id = ? AND user_id = ?`, id, uid)))
}

// ProjectByKey finds a project by its DSN key (whoever owns it).
func (s *Store) ProjectByKey(ctx context.Context, key string) (*Project, error) {
	return notFoundP(scanProject(s.DB.QueryRowContext(ctx, `SELECT `+projectCols+` FROM projects WHERE public_key = ?`, key)))
}

// ProjectByContainer is the person's project for a watched container.
func (s *Store) ProjectByContainer(ctx context.Context, uid int64, name string) (*Project, error) {
	return notFoundP(scanProject(s.DB.QueryRowContext(ctx, `SELECT `+projectCols+` FROM projects WHERE user_id = ? AND container = ?`, uid, name)))
}

// ProjectBySource is the person's project for a Pocket app reporting through a link.
func (s *Store) ProjectBySource(ctx context.Context, uid int64, source string) (*Project, error) {
	return notFoundP(scanProject(s.DB.QueryRowContext(ctx, `SELECT `+projectCols+` FROM projects WHERE user_id = ? AND source = ?`, uid, source)))
}

func notFoundP(p *Project, err error) (*Project, error) {
	if err != nil {
		return nil, notFound(err)
	}
	return p, nil
}

// ListProjects is the person's projects with their open issues, the events
// of the last 24 hours and those hours one by one (oldest first).
func (s *Store) ListProjects(ctx context.Context, uid int64, archived bool) ([]*Project, error) {
	q := `SELECT ` + projectCols + ` FROM projects WHERE user_id = ?`
	if !archived {
		q += ` AND archived = 0`
	}
	rows, err := s.DB.QueryContext(ctx, q+` ORDER BY archived, name COLLATE NOCASE`, uid)
	if err != nil {
		return nil, err
	}
	out := []*Project{}
	byID := map[int64]*Project{}
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		p.Hours = make([]int, 24)
		out = append(out, p)
		byID[p.ID] = p
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows, err = s.DB.QueryContext(ctx, `SELECT project_id, COUNT(1) FROM issues WHERE user_id = ? AND status = 'open' GROUP BY project_id`, uid)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id int64
		var n int
		if rows.Scan(&id, &n) == nil && byID[id] != nil {
			byID[id].Open = n
		}
	}
	rows.Close()
	start := time.Now().UTC().Add(-23 * time.Hour).Truncate(time.Hour)
	rows, err = s.DB.QueryContext(ctx, `SELECT i.project_id, h.hour, SUM(h.n) FROM issue_hours h JOIN issues i ON i.id = h.issue_id
		WHERE h.user_id = ? AND h.hour >= ? GROUP BY i.project_id, h.hour`, uid, HourKey(start))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var hour string
		var n int
		if rows.Scan(&id, &hour, &n) != nil || byID[id] == nil {
			continue
		}
		if k := hourIndex(start, hour, 24); k >= 0 {
			byID[id].Hours[k] += n
			byID[id].Day += n
		}
	}
	return out, rows.Err()
}

// UpdateProject saves a project's name and choices.
func (s *Store) UpdateProject(ctx context.Context, p *Project) error {
	p.UpdatedAt = Now()
	res, err := s.DB.ExecContext(ctx, `UPDATE projects SET name = ?, platform = ?, public_key = ?, app_slug = ?, notify_new = ?, notify_again = ?, notify_spike = ?,
		min_level = ?, rate_limit = ?, archived = ?, updated_at = ? WHERE id = ? AND user_id = ?`,
		p.Name, p.Platform, p.PublicKey, p.AppSlug, p.NotifyNew, p.NotifyAgain, p.NotifySpike, p.MinLevel, p.RateLimit, p.Archived, p.UpdatedAt, p.ID, p.UserID)
	return affected(res, err)
}

// DeleteProject removes a project with its issues and events.
func (s *Store) DeleteProject(ctx context.Context, uid, id int64) error {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM projects WHERE id = ? AND user_id = ?`, id, uid)
	return affected(res, err)
}

func affected(res sql.Result, err error) error {
	if err != nil {
		return conflict(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// HourKey is the hour bucket a time falls in (UTC).
func HourKey(t time.Time) string { return t.UTC().Format("2006-01-02T15") }

// hourIndex is where an hour key falls among n hours from start, -1 outside.
func hourIndex(start time.Time, key string, n int) int {
	t, err := time.Parse("2006-01-02T15", key)
	if err != nil {
		return -1
	}
	k := int(t.Sub(start) / time.Hour)
	if k < 0 || k >= n {
		return -1
	}
	return k
}

// MatchName reports whether a container name matches a pattern list
// ("pocket-*, caddy"; * stands for anything).
func MatchName(pattern, name string) bool {
	for _, p := range strings.Split(pattern, ",") {
		p = strings.TrimSpace(p)
		if p != "" && glob(p, name) {
			return true
		}
	}
	return false
}

func glob(p, s string) bool {
	if p == "*" {
		return true
	}
	parts := strings.Split(p, "*")
	if len(parts) == 1 {
		return p == s
	}
	if !strings.HasPrefix(s, parts[0]) {
		return false
	}
	s = s[len(parts[0]):]
	for i := 1; i < len(parts)-1; i++ {
		k := strings.Index(s, parts[i])
		if k < 0 {
			return false
		}
		s = s[k+len(parts[i]):]
	}
	return strings.HasSuffix(s, parts[len(parts)-1])
}

// ProjectEventsBetween counts a project's errors in a time span (by the hour).
func (s *Store) ProjectEventsBetween(ctx context.Context, uid, id int64, from, to time.Time) (int, error) {
	var n int
	err := s.DB.QueryRowContext(ctx, `SELECT COALESCE(SUM(h.n),0) FROM issue_hours h JOIN issues i ON i.id = h.issue_id
		WHERE h.user_id = ? AND i.project_id = ? AND h.hour >= ? AND h.hour < ?`, uid, id, HourKey(from), HourKey(to)).Scan(&n)
	return n, err
}

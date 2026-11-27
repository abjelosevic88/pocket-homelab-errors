package store

import (
	"context"
	"time"
)

// What Pocket Search, Day and Hub read (the capability routes): issue titles,
// projects, counts and times, never an event's contents.

// DayIssue is an issue first seen, or back after it was resolved, in a span.
type DayIssue struct {
	ID          int64
	Title       string
	ProjectName string
	Status      string
	FirstSeen   string
	BackAt      string // when it last came back, "" = never (or before this was kept)
	Events      int    // how many times it happened in the span, by the hour
}

// DayIssues are the person's issues first seen in [from, to) or that came
// back in it, with how many times each happened then (at most 200).
func (s *Store) DayIssues(ctx context.Context, uid int64, from, to time.Time) ([]DayIssue, error) {
	a, b := Stamp(from), Stamp(to)
	rows, err := s.DB.QueryContext(ctx, `SELECT i.id, i.title, p.name, i.status, i.first_seen, COALESCE(i.regressed_at, ''),
		(SELECT COALESCE(SUM(h.n), 0) FROM issue_hours h WHERE h.issue_id = i.id AND h.hour >= ? AND h.hour < ?)
		FROM issues i JOIN projects p ON p.id = i.project_id
		WHERE i.user_id = ? AND ((i.first_seen >= ? AND i.first_seen < ?) OR (i.regressed_at >= ? AND i.regressed_at < ?))
		ORDER BY i.first_seen LIMIT 200`, HourKey(from), HourKey(to), uid, a, b, a, b)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DayIssue{}
	for rows.Next() {
		var d DayIssue
		if err := rows.Scan(&d.ID, &d.Title, &d.ProjectName, &d.Status, &d.FirstSeen, &d.BackAt, &d.Events); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// BusyIssue is an open issue with how many times it happened since a time.
type BusyIssue struct {
	ID          int64
	Title       string
	ProjectName string
	FirstSeen   string
	Events      int
}

// BusyOpenIssues are the person's open issues that happened since a time (by
// the hour): the ones first seen since then first, then the busiest.
func (s *Store) BusyOpenIssues(ctx context.Context, uid int64, since time.Time, limit int) ([]BusyIssue, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT i.id, i.title, p.name, i.first_seen, SUM(h.n) AS n
		FROM issue_hours h JOIN issues i ON i.id = h.issue_id JOIN projects p ON p.id = i.project_id
		WHERE h.user_id = ? AND h.hour >= ? AND i.status = 'open'
		GROUP BY i.id ORDER BY i.first_seen >= ? DESC, n DESC, i.last_seen DESC LIMIT ?`, uid, HourKey(since), Stamp(since), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []BusyIssue{}
	for rows.Next() {
		var b BusyIssue
		if err := rows.Scan(&b.ID, &b.Title, &b.ProjectName, &b.FirstSeen, &b.Events); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// OpenSince counts the person's open issues, the ones of them first seen
// since a time and the ones that came back since then.
func (s *Store) OpenSince(ctx context.Context, uid int64, since time.Time) (open, fresh, back int, err error) {
	at := Stamp(since)
	err = s.DB.QueryRowContext(ctx, `SELECT COUNT(1), COALESCE(SUM(first_seen >= ?), 0), COALESCE(SUM(regressed = 1 AND first_seen < ? AND regressed_at >= ?), 0)
		FROM issues WHERE user_id = ? AND status = 'open'`, at, at, at, uid).Scan(&open, &fresh, &back)
	return open, fresh, back, err
}

// EventsBetween counts the person's errors in a span, every issue's, by the hour.
func (s *Store) EventsBetween(ctx context.Context, uid int64, from, to time.Time) (int, error) {
	var n int
	err := s.DB.QueryRowContext(ctx, `SELECT COALESCE(SUM(n), 0) FROM issue_hours WHERE user_id = ? AND hour >= ? AND hour < ?`, uid, HourKey(from), HourKey(to)).Scan(&n)
	return n, err
}

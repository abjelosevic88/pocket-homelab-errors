package store

import (
	"context"
	"database/sql"
	"encoding/json"
)

// IssueByFingerprint is a project's issue for a fingerprint.
func (s *Store) IssueByFingerprint(ctx context.Context, projectID int64, fp string) (*Issue, error) {
	is, err := scanIssue(s.DB.QueryRowContext(ctx, `SELECT `+issueCols+` FROM issues WHERE project_id = ? AND fingerprint = ?`, projectID, fp))
	if err != nil {
		return nil, notFound(err)
	}
	return is, nil
}

// ImportIssue adds an issue as it was exported (into the person's project p).
func (s *Store) ImportIssue(ctx context.Context, p *Project, is *Issue) error {
	if is.Status == "" {
		is.Status = "open"
	}
	if is.FirstSeen == "" {
		is.FirstSeen = Now()
	}
	if is.LastSeen == "" {
		is.LastSeen = is.FirstSeen
	}
	res, err := s.DB.ExecContext(ctx, `INSERT INTO issues (user_id, project_id, fingerprint, title, culprit, type, level, status, regressed, times, first_seen, last_seen,
		first_release, last_release, environment, resolved_at, ignored_until, note, ai_text, ai_at, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		p.UserID, p.ID, is.Fingerprint, is.Title, is.Culprit, is.Type, is.Level, is.Status, is.Regressed, is.Times, is.FirstSeen, is.LastSeen, is.FirstRelease,
		is.LastRelease, is.Environment, is.ResolvedAt, is.IgnoredUntil, is.Note, is.AIText, is.AIAt, Now())
	if err != nil {
		return conflict(err)
	}
	is.ID, _ = res.LastInsertId()
	is.UserID, is.ProjectID = p.UserID, p.ID
	return nil
}

// ImportEvent adds an exported event to an issue unless its event id is
// already there; it says whether it was added.
func (s *Store) ImportEvent(ctx context.Context, is *Issue, e *Event) (bool, error) {
	if e.EventID != "" {
		var one int
		err := s.DB.QueryRowContext(ctx, `SELECT 1 FROM events WHERE project_id = ? AND event_id = ?`, is.ProjectID, e.EventID).Scan(&one)
		if err == nil {
			return false, nil
		}
		if err != sql.ErrNoRows {
			return false, err
		}
	}
	data, _ := json.Marshal(e.Data)
	if e.ReceivedAt == "" {
		e.ReceivedAt = e.At
	}
	_, err := s.DB.ExecContext(ctx, `INSERT INTO events (user_id, project_id, issue_id, event_id, at, received_at, level, message, environment, release, platform, who, data)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`, is.UserID, is.ProjectID, is.ID, e.EventID, e.At, e.ReceivedAt, e.Level, e.Message, e.Environment, e.Release,
		e.Platform, e.Who, string(data))
	return err == nil, err
}

// AddHours puts back an issue's counts per hour (adding to what is there).
func (s *Store) AddHours(ctx context.Context, is *Issue, hours map[string]int) error {
	for h, n := range hours {
		if _, err := s.DB.ExecContext(ctx, `INSERT INTO issue_hours (issue_id, user_id, hour, n) VALUES (?, ?, ?, ?)
			ON CONFLICT(issue_id, hour) DO UPDATE SET n = MAX(n, excluded.n)`, is.ID, is.UserID, h, n); err != nil {
			return err
		}
	}
	return nil
}

// AllHours is an issue's counts per hour.
func (s *Store) AllHours(ctx context.Context, issueID int64) (map[string]int, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT hour, n FROM issue_hours WHERE issue_id = ?`, issueID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var h string
		var n int
		if err := rows.Scan(&h, &n); err != nil {
			return nil, err
		}
		out[h] = n
	}
	return out, rows.Err()
}

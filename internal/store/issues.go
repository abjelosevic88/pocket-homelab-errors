package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"
)

// Incoming is one error on its way in, already grouped (Fingerprint) and
// titled by the server.
type Incoming struct {
	EventID     string
	At          time.Time
	Fingerprint string
	Title       string
	Culprit     string
	Type        string
	Level       string
	Message     string
	Environment string
	Release     string
	Platform    string
	Who         string
	Data        EventData
}

// Outcome says what an event did to its issue.
type Outcome string

const (
	OutcomeNew       Outcome = "new"       // a problem never seen before
	OutcomeRegressed Outcome = "regressed" // resolved, and back
	OutcomeReopened  Outcome = "reopened"  // ignored for a while, and the while is over
	OutcomeSeen      Outcome = "seen"      // happened again
	OutcomeDuplicate Outcome = "duplicate" // the same event sent twice
)

// RecordResult is what Record did.
type RecordResult struct {
	Issue   *Issue
	Outcome Outcome
	Kept    bool // the event itself was kept (busy issues keep only some)
	HourN   int  // the issue's events in this hour so far
}

// eventsPerMinute is how many events of one issue are kept a minute; the
// rest are only counted.
const eventsPerMinute = 20

// eventsPerIssue is how many of an issue's newest events are kept.
const eventsPerIssue = 200

// Record files an event under its issue (creating or reopening it) in one
// transaction, and counts it for the charts.
func (s *Store) Record(ctx context.Context, p *Project, in Incoming) (RecordResult, error) {
	var out RecordResult
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer func() { _ = tx.Rollback() }()
	if in.EventID != "" {
		var one int
		if tx.QueryRowContext(ctx, `SELECT 1 FROM events WHERE project_id = ? AND event_id = ?`, p.ID, in.EventID).Scan(&one) == nil {
			out.Outcome = OutcomeDuplicate
			return out, nil
		}
	}
	at := Stamp(in.At)
	now := time.Now().UTC()
	if in.Level == "" {
		in.Level = "error"
	}
	is, err := scanIssue(tx.QueryRowContext(ctx, `SELECT `+issueCols+` FROM issues WHERE project_id = ? AND fingerprint = ?`, p.ID, in.Fingerprint))
	switch {
	case err == sql.ErrNoRows:
		is = &Issue{UserID: p.UserID, ProjectID: p.ID, Fingerprint: in.Fingerprint, Title: in.Title, Culprit: in.Culprit, Type: in.Type, Level: in.Level,
			Status: "open", Times: 1, FirstSeen: at, LastSeen: at, FirstRelease: in.Release, LastRelease: in.Release, Environment: in.Environment}
		res, err := tx.ExecContext(ctx, `INSERT INTO issues (user_id, project_id, fingerprint, title, culprit, type, level, status, times, first_seen, last_seen,
			first_release, last_release, environment, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			is.UserID, is.ProjectID, is.Fingerprint, is.Title, is.Culprit, is.Type, is.Level, is.Status, is.Times, is.FirstSeen, is.LastSeen,
			is.FirstRelease, is.LastRelease, is.Environment, Now())
		if err != nil {
			return out, err
		}
		is.ID, _ = res.LastInsertId()
		out.Outcome = OutcomeNew
	case err != nil:
		return out, err
	default:
		out.Outcome = OutcomeSeen
		switch is.Status {
		case "resolved":
			is.Status, is.Regressed, is.ResolvedAt = "open", true, nil
			out.Outcome = OutcomeRegressed
		case "ignored":
			if is.IgnoredUntil != nil && *is.IgnoredUntil <= Stamp(now) {
				is.Status, is.IgnoredUntil = "open", nil
				out.Outcome = OutcomeReopened
			}
		}
		is.Times++
		if at > is.LastSeen {
			is.LastSeen = at
			if in.Release != "" {
				is.LastRelease = in.Release
			}
			if in.Environment != "" {
				is.Environment = in.Environment
			}
		}
		if at < is.FirstSeen {
			is.FirstSeen = at
		}
		if LevelRank(in.Level) < LevelRank(is.Level) {
			is.Level = in.Level
		}
		if is.Culprit == "" {
			is.Culprit = in.Culprit
		}
		if _, err := tx.ExecContext(ctx, `UPDATE issues SET status = ?, regressed = ?, resolved_at = ?, ignored_until = ?, times = ?, first_seen = ?, last_seen = ?,
			last_release = ?, environment = ?, level = ?, culprit = ?, updated_at = ? WHERE id = ?`,
			is.Status, is.Regressed, is.ResolvedAt, is.IgnoredUntil, is.Times, is.FirstSeen, is.LastSeen, is.LastRelease, is.Environment, is.Level,
			is.Culprit, Now(), is.ID); err != nil {
			return out, err
		}
	}
	hour := HourKey(in.At)
	if in.At.After(now.Add(time.Hour)) {
		hour = HourKey(now)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO issue_hours (issue_id, user_id, hour, n) VALUES (?, ?, ?, 1)
		ON CONFLICT(issue_id, hour) DO UPDATE SET n = n + 1`, is.ID, p.UserID, hour); err != nil {
		return out, err
	}
	_ = tx.QueryRowContext(ctx, `SELECT n FROM issue_hours WHERE issue_id = ? AND hour = ?`, is.ID, HourKey(now)).Scan(&out.HourN)
	if in.Who != "" {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO issue_people (issue_id, who) VALUES (?, ?)`, is.ID, in.Who); err != nil {
			return out, err
		}
	}
	var recent int
	_ = tx.QueryRowContext(ctx, `SELECT COUNT(1) FROM events WHERE issue_id = ? AND received_at > ?`, is.ID, Stamp(now.Add(-time.Minute))).Scan(&recent)
	if recent < eventsPerMinute {
		data, _ := json.Marshal(in.Data)
		if _, err := tx.ExecContext(ctx, `INSERT INTO events (user_id, project_id, issue_id, event_id, at, received_at, level, message, environment, release, platform, who, data)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`, p.UserID, p.ID, is.ID, in.EventID, at, Stamp(now), in.Level, in.Message, in.Environment, in.Release,
			in.Platform, in.Who, string(data)); err != nil {
			return out, err
		}
		out.Kept = true
		if is.Times%25 == 0 {
			if _, err := tx.ExecContext(ctx, `DELETE FROM events WHERE issue_id = ? AND id NOT IN (SELECT id FROM events WHERE issue_id = ? ORDER BY at DESC, id DESC LIMIT ?)`,
				is.ID, is.ID, eventsPerIssue); err != nil {
				return out, err
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE projects SET last_event_at = ? WHERE id = ?`, Stamp(now), p.ID); err != nil {
		return out, err
	}
	if err := tx.Commit(); err != nil {
		return out, err
	}
	out.Issue = is
	return out, nil
}

const issueCols = `id, user_id, project_id, fingerprint, title, culprit, type, level, status, regressed, times, first_seen, last_seen,
	first_release, last_release, environment, resolved_at, ignored_until, note, ai_text, ai_at, spike_at, updated_at`

func scanIssue(sc interface{ Scan(...any) error }) (*Issue, error) {
	is := &Issue{}
	var res, ign, aiAt, spike sql.NullString
	err := sc.Scan(&is.ID, &is.UserID, &is.ProjectID, &is.Fingerprint, &is.Title, &is.Culprit, &is.Type, &is.Level, &is.Status, &is.Regressed, &is.Times,
		&is.FirstSeen, &is.LastSeen, &is.FirstRelease, &is.LastRelease, &is.Environment, &res, &ign, &is.Note, &is.AIText, &aiAt, &spike, &is.UpdatedAt)
	is.ResolvedAt, is.IgnoredUntil, is.AIAt, is.SpikeAt = strP(res), strP(ign), strP(aiAt), strP(spike)
	return is, err
}

func strP(v sql.NullString) *string {
	if !v.Valid {
		return nil
	}
	return &v.String
}

// IssueFilter narrows the issue list.
type IssueFilter struct {
	Status    string // open | resolved | ignored | all ("" = open)
	ProjectID int64
	Level     string // only this level and worse
	Q         string
	Since     string // last seen at or after (UTC stamp)
	Sort      string // last | first | times | people
	Limit     int
	Offset    int
}

// ListIssues is the person's issues, with the project, how many of the
// app's people it reached and the last 24 hours. The total is before the limit.
func (s *Store) ListIssues(ctx context.Context, uid int64, f IssueFilter) ([]*Issue, int, error) {
	where := []string{"i.user_id = ?"}
	args := []any{uid}
	switch f.Status {
	case "", "open":
		where = append(where, "i.status = 'open'")
	case "resolved", "ignored":
		where = append(where, "i.status = ?")
		args = append(args, f.Status)
	}
	if f.ProjectID > 0 {
		where = append(where, "i.project_id = ?")
		args = append(args, f.ProjectID)
	}
	if f.Level != "" {
		lv := []string{}
		for _, l := range Levels {
			lv = append(lv, "'"+l+"'")
			if l == f.Level {
				break
			}
		}
		where = append(where, "i.level IN ("+strings.Join(lv, ",")+")")
	}
	if f.Since != "" {
		where = append(where, "i.last_seen >= ?")
		args = append(args, f.Since)
	}
	for _, w := range strings.Fields(strings.ToLower(f.Q)) {
		where = append(where, "(LOWER(i.title) LIKE ? OR LOWER(i.culprit) LIKE ? OR LOWER(i.type) LIKE ? OR LOWER(p.name) LIKE ? OR LOWER(i.note) LIKE ?)")
		like := "%" + w + "%"
		args = append(args, like, like, like, like, like)
	}
	cond := strings.Join(where, " AND ")
	var total int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(1) FROM issues i JOIN projects p ON p.id = i.project_id WHERE `+cond, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	order := "i.last_seen DESC"
	switch f.Sort {
	case "first":
		order = "i.first_seen DESC"
	case "times":
		order = "i.times DESC, i.last_seen DESC"
	case "people":
		order = "(SELECT COUNT(1) FROM issue_people x WHERE x.issue_id = i.id) DESC, i.last_seen DESC"
	}
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 100
	}
	cols := "i." + strings.Join(strings.Fields(strings.ReplaceAll(issueCols, ",", " ")), ", i.")
	rows, err := s.DB.QueryContext(ctx, `SELECT `+cols+`, p.name, p.app_slug FROM issues i JOIN projects p ON p.id = i.project_id WHERE `+cond+
		` ORDER BY `+order+` LIMIT ? OFFSET ?`, append(args, f.Limit, f.Offset)...)
	if err != nil {
		return nil, 0, err
	}
	out := []*Issue{}
	for rows.Next() {
		is := &Issue{}
		var res, ign, aiAt, spike sql.NullString
		if err := rows.Scan(&is.ID, &is.UserID, &is.ProjectID, &is.Fingerprint, &is.Title, &is.Culprit, &is.Type, &is.Level, &is.Status, &is.Regressed, &is.Times,
			&is.FirstSeen, &is.LastSeen, &is.FirstRelease, &is.LastRelease, &is.Environment, &res, &ign, &is.Note, &is.AIText, &aiAt, &spike, &is.UpdatedAt,
			&is.ProjectName, &is.AppSlug); err != nil {
			rows.Close()
			return nil, 0, err
		}
		is.ResolvedAt, is.IgnoredUntil, is.AIAt, is.SpikeAt = strP(res), strP(ign), strP(aiAt), strP(spike)
		out = append(out, is)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return out, total, s.fillIssues(ctx, uid, out)
}

// fillIssues adds people, the last 24 hours and "new" to issues.
func (s *Store) fillIssues(ctx context.Context, uid int64, list []*Issue) error {
	if len(list) == 0 {
		return nil
	}
	byID := map[int64]*Issue{}
	ids := make([]any, 0, len(list)+2)
	marks := make([]string, 0, len(list))
	newSince := Stamp(time.Now().Add(-24 * time.Hour))
	for _, is := range list {
		is.Hours = make([]int, 24)
		is.New = is.FirstSeen >= newSince
		byID[is.ID] = is
		ids = append(ids, is.ID)
		marks = append(marks, "?")
	}
	in := "(" + strings.Join(marks, ",") + ")"
	rows, err := s.DB.QueryContext(ctx, `SELECT issue_id, COUNT(1) FROM issue_people WHERE issue_id IN `+in+` GROUP BY issue_id`, ids...)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id int64
		var n int
		if rows.Scan(&id, &n) == nil && byID[id] != nil {
			byID[id].People = n
		}
	}
	rows.Close()
	start := time.Now().UTC().Add(-23 * time.Hour).Truncate(time.Hour)
	rows, err = s.DB.QueryContext(ctx, `SELECT issue_id, hour, n FROM issue_hours WHERE user_id = ? AND hour >= ? AND issue_id IN `+in,
		append([]any{uid, HourKey(start)}, ids...)...)
	if err != nil {
		return err
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
	return rows.Err()
}

// GetIssue is one of the person's issues, filled in like a list row.
func (s *Store) GetIssue(ctx context.Context, uid, id int64) (*Issue, error) {
	is, err := scanIssue(s.DB.QueryRowContext(ctx, `SELECT `+issueCols+` FROM issues WHERE id = ? AND user_id = ?`, id, uid))
	if err != nil {
		return nil, notFound(err)
	}
	_ = s.DB.QueryRowContext(ctx, `SELECT name, app_slug FROM projects WHERE id = ?`, is.ProjectID).Scan(&is.ProjectName, &is.AppSlug)
	return is, s.fillIssues(ctx, uid, []*Issue{is})
}

// SetStatus resolves, ignores (until a time, or for good with "") or
// reopens issues; returns the ones changed.
func (s *Store) SetStatus(ctx context.Context, uid int64, ids []int64, status, until string) ([]int64, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	now := Now()
	changed := []int64{}
	for _, id := range ids {
		var res sql.Result
		switch status {
		case "resolved":
			res, err = tx.ExecContext(ctx, `UPDATE issues SET status = 'resolved', resolved_at = ?, ignored_until = NULL, updated_at = ? WHERE id = ? AND user_id = ?`, now, now, id, uid)
		case "ignored":
			var u any
			if until != "" {
				u = until
			}
			res, err = tx.ExecContext(ctx, `UPDATE issues SET status = 'ignored', ignored_until = ?, resolved_at = NULL, updated_at = ? WHERE id = ? AND user_id = ?`, u, now, id, uid)
		default:
			res, err = tx.ExecContext(ctx, `UPDATE issues SET status = 'open', regressed = 0, resolved_at = NULL, ignored_until = NULL, updated_at = ? WHERE id = ? AND user_id = ?`, now, id, uid)
		}
		if err != nil {
			return nil, err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			changed = append(changed, id)
		}
	}
	return changed, tx.Commit()
}

// SetNote saves the person's note on an issue.
func (s *Store) SetNote(ctx context.Context, uid, id int64, note string) error {
	res, err := s.DB.ExecContext(ctx, `UPDATE issues SET note = ?, updated_at = ? WHERE id = ? AND user_id = ?`, note, Now(), id, uid)
	return affected(res, err)
}

// SetAI keeps Pocket AI's explanation of an issue.
func (s *Store) SetAI(ctx context.Context, uid, id int64, text string) error {
	res, err := s.DB.ExecContext(ctx, `UPDATE issues SET ai_text = ?, ai_at = ? WHERE id = ? AND user_id = ?`, text, Now(), id, uid)
	return affected(res, err)
}

// MarkSpike notes that a spike was told now.
func (s *Store) MarkSpike(ctx context.Context, id int64) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE issues SET spike_at = ? WHERE id = ?`, Now(), id)
	return err
}

// DeleteIssue removes an issue and its events (if it happens again it comes back as new).
func (s *Store) DeleteIssue(ctx context.Context, uid, id int64) error {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM issues WHERE id = ? AND user_id = ?`, id, uid)
	return affected(res, err)
}

// IssueHours is an issue's events per hour (or, by day, per local day)
// from a time on.
func (s *Store) IssueHours(ctx context.Context, uid, id int64, from time.Time, n int, byDay bool) ([]int, error) {
	out := make([]int, n)
	rows, err := s.DB.QueryContext(ctx, `SELECT hour, n FROM issue_hours WHERE user_id = ? AND issue_id = ? AND hour >= ?`, uid, id, HourKey(from))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var hour string
		var c int
		if rows.Scan(&hour, &c) != nil {
			continue
		}
		t, err := time.Parse("2006-01-02T15", hour)
		if err != nil {
			continue
		}
		k := int(t.Sub(from) / time.Hour)
		if byDay {
			lt := t.In(time.Local)
			k = int(time.Date(lt.Year(), lt.Month(), lt.Day(), 0, 0, 0, 0, time.Local).Sub(from).Hours()+0.5) / 24
		}
		if k >= 0 && k < n {
			out[k] += c
		}
	}
	return out, rows.Err()
}

// Overview is the person's errors at a glance.
type Overview struct {
	Open      int            `json:"open"`
	New       int            `json:"new"`       // first seen in the last 24 hours
	Regressed int            `json:"regressed"` // came back and still open
	Events    int            `json:"events_24h"`
	Hours     map[string]any `json:"hours"` // levels → 24 numbers, oldest first
	Projects  int            `json:"projects"`
	Quiet     int            `json:"quiet_projects"` // projects with nothing open
}

// GetOverview counts the open issues and the last 24 hours by level.
func (s *Store) GetOverview(ctx context.Context, uid int64) (Overview, error) {
	o := Overview{Hours: map[string]any{}}
	since := Stamp(time.Now().Add(-24 * time.Hour))
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(1), COALESCE(SUM(first_seen >= ?),0), COALESCE(SUM(regressed),0) FROM issues WHERE user_id = ? AND status = 'open'`,
		since, uid).Scan(&o.Open, &o.New, &o.Regressed); err != nil {
		return o, err
	}
	_ = s.DB.QueryRowContext(ctx, `SELECT COUNT(1), COALESCE(SUM(NOT EXISTS (SELECT 1 FROM issues i WHERE i.project_id = p.id AND i.status = 'open')),0)
		FROM projects p WHERE p.user_id = ? AND p.archived = 0`, uid).Scan(&o.Projects, &o.Quiet)
	start := time.Now().UTC().Add(-23 * time.Hour).Truncate(time.Hour)
	series := map[string][]int{}
	for _, l := range []string{"fatal", "error", "warning", "info"} {
		series[l] = make([]int, 24)
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT i.level, h.hour, SUM(h.n) FROM issue_hours h JOIN issues i ON i.id = h.issue_id
		WHERE h.user_id = ? AND h.hour >= ? GROUP BY i.level, h.hour`, uid, HourKey(start))
	if err != nil {
		return o, err
	}
	defer rows.Close()
	for rows.Next() {
		var lv, hour string
		var n int
		if rows.Scan(&lv, &hour, &n) != nil {
			continue
		}
		if lv == "debug" {
			lv = "info"
		}
		if k := hourIndex(start, hour, 24); k >= 0 && series[lv] != nil {
			series[lv][k] += n
			o.Events += n
		}
	}
	for k, v := range series {
		o.Hours[k] = v
	}
	return o, rows.Err()
}

// ---- events ----

const eventCols = `id, user_id, project_id, issue_id, event_id, at, received_at, level, message, environment, release, platform, who, data`

func scanEvent(sc interface{ Scan(...any) error }) (*Event, error) {
	e := &Event{}
	var data string
	if err := sc.Scan(&e.ID, &e.UserID, &e.ProjectID, &e.IssueID, &e.EventID, &e.At, &e.ReceivedAt, &e.Level, &e.Message, &e.Environment, &e.Release,
		&e.Platform, &e.Who, &data); err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(data), &e.Data)
	return e, nil
}

// ListEvents is an issue's kept events, newest first (before = an event id
// to page on).
func (s *Store) ListEvents(ctx context.Context, uid, issueID int64, before int64, limit int) ([]*Event, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	q := `SELECT ` + eventCols + ` FROM events WHERE user_id = ? AND issue_id = ?`
	args := []any{uid, issueID}
	if before > 0 {
		q += ` AND id < ?`
		args = append(args, before)
	}
	rows, err := s.DB.QueryContext(ctx, q+` ORDER BY id DESC LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Event{}
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// GetEvent is one event of an issue; id 0 = the newest.
func (s *Store) GetEvent(ctx context.Context, uid, issueID, id int64) (*Event, error) {
	q := `SELECT ` + eventCols + ` FROM events WHERE user_id = ? AND issue_id = ?`
	args := []any{uid, issueID}
	if id > 0 {
		q += ` AND id = ?`
		args = append(args, id)
	}
	e, err := scanEvent(s.DB.QueryRowContext(ctx, q+` ORDER BY id DESC LIMIT 1`, args...))
	return e, notFound(err)
}

// EventPlace says where an event sits among its issue's kept events: its
// number counted from the newest (1), the count, and the newer and older ids.
func (s *Store) EventPlace(ctx context.Context, uid, issueID, id int64) (pos, total int, newer, older int64) {
	_ = s.DB.QueryRowContext(ctx, `SELECT COUNT(1) FROM events WHERE user_id = ? AND issue_id = ?`, uid, issueID).Scan(&total)
	_ = s.DB.QueryRowContext(ctx, `SELECT COUNT(1) FROM events WHERE user_id = ? AND issue_id = ? AND id >= ?`, uid, issueID, id).Scan(&pos)
	_ = s.DB.QueryRowContext(ctx, `SELECT COALESCE(MIN(id),0) FROM events WHERE user_id = ? AND issue_id = ? AND id > ?`, uid, issueID, id).Scan(&newer)
	_ = s.DB.QueryRowContext(ctx, `SELECT COALESCE(MAX(id),0) FROM events WHERE user_id = ? AND issue_id = ? AND id < ?`, uid, issueID, id).Scan(&older)
	return
}

// TagCounts is, per tag key, the values on an issue's kept events and how often.
func (s *Store) TagCounts(ctx context.Context, uid, issueID int64) (map[string]map[string]int, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT data, environment, release FROM events WHERE user_id = ? AND issue_id = ? ORDER BY id DESC LIMIT 200`, uid, issueID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]map[string]int{}
	add := func(k, v string) {
		if v == "" {
			return
		}
		if out[k] == nil {
			out[k] = map[string]int{}
		}
		out[k][v]++
	}
	for rows.Next() {
		var data, env, rel string
		if rows.Scan(&data, &env, &rel) != nil {
			continue
		}
		var d struct {
			Tags [][2]string `json:"tags"`
		}
		_ = json.Unmarshal([]byte(data), &d)
		add("environment", env)
		add("release", rel)
		for _, t := range d.Tags {
			if t[0] != "environment" && t[0] != "release" {
				add(t[0], t[1])
			}
		}
	}
	return out, rows.Err()
}

// ---- links to the other apps ----

// AddLink notes what an issue became in another app.
func (s *Store) AddLink(ctx context.Context, uid int64, l *IssueLink) error {
	l.CreatedAt = Now()
	res, err := s.DB.ExecContext(ctx, `INSERT INTO issue_links (user_id, issue_id, app, ref, title, url, created_at) VALUES (?,?,?,?,?,?,?)`,
		uid, l.IssueID, l.App, l.Ref, l.Title, l.URL, l.CreatedAt)
	if err != nil {
		return err
	}
	l.ID, _ = res.LastInsertId()
	return nil
}

// ListLinks is what an issue became elsewhere (issueID 0 = all of the person's).
func (s *Store) ListLinks(ctx context.Context, uid, issueID int64) ([]*IssueLink, error) {
	q := `SELECT id, issue_id, app, ref, title, url, created_at FROM issue_links WHERE user_id = ?`
	args := []any{uid}
	if issueID > 0 {
		q += ` AND issue_id = ?`
		args = append(args, issueID)
	}
	rows, err := s.DB.QueryContext(ctx, q+` ORDER BY id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*IssueLink{}
	for rows.Next() {
		l := &IssueLink{}
		if err := rows.Scan(&l.ID, &l.IssueID, &l.App, &l.Ref, &l.Title, &l.URL, &l.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// DeleteLink forgets one link (the item in the other app stays).
func (s *Store) DeleteLink(ctx context.Context, uid, issueID, id int64) error {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM issue_links WHERE id = ? AND issue_id = ? AND user_id = ?`, id, issueID, uid)
	return affected(res, err)
}

// ---- tidying ----

// Tidy drops one person's events older than keepDays, hour counts older
// than a year, and closed issues not seen for three times as long.
func (s *Store) Tidy(ctx context.Context, uid int64, keepDays int) (int64, error) {
	if keepDays < 1 {
		keepDays = 30
	}
	cut := time.Now().Add(-time.Duration(keepDays) * 24 * time.Hour)
	res, err := s.DB.ExecContext(ctx, `DELETE FROM events WHERE user_id = ? AND received_at < ?`, uid, Stamp(cut))
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	if _, err := s.DB.ExecContext(ctx, `DELETE FROM issue_hours WHERE user_id = ? AND hour < ?`, uid, HourKey(time.Now().AddDate(-1, 0, 0))); err != nil {
		return n, err
	}
	old := time.Now().Add(-3 * time.Duration(keepDays) * 24 * time.Hour)
	_, err = s.DB.ExecContext(ctx, `DELETE FROM issues WHERE user_id = ? AND status <> 'open' AND last_seen < ?`, uid, Stamp(old))
	return n, err
}

package logs

import (
	"context"
	"database/sql"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Line is one kept line as the API gives it.
type Line struct {
	ID      int64             `json:"id"`
	At      int64             `json:"at"` // unix ms
	Source  int64             `json:"source_id"`
	Name    string            `json:"source"`
	Label   string            `json:"source_label"`
	Group   string            `json:"group"`
	Level   string            `json:"level"`
	Stream  string            `json:"stream,omitempty"`
	Pattern string            `json:"pattern"` // an id, as text (too big for JavaScript's numbers)
	Msg     string            `json:"msg"`
	Attrs   map[string]string `json:"attrs,omitempty"`
}

// Filter picks lines.
type Filter struct {
	Q        string
	Sources  []int64
	Groups   []string
	MinLevel int
	From, To int64 // unix ms, 0 = open
	Pattern  int64
	Before   int64 // a line id: older than it
	After    int64 // a line id: newer than it (live)
	Limit    int
}

// FTSQuery turns what someone typed into an FTS5 query: every word must be
// there (the last one may go on), "-word" must not, quotes keep a phrase.
func FTSQuery(q string) string {
	var pos, neg []string
	for _, w := range splitWords(q) {
		not := strings.HasPrefix(w, "-") && len(w) > 1
		if not {
			w = w[1:]
		}
		w = strings.Trim(w, `"`)
		toks := strings.FieldsFunc(w, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' })
		if len(toks) == 0 {
			continue
		}
		ph := `"` + strings.Join(toks, " ") + `"`
		if not {
			neg = append(neg, ph)
		} else {
			pos = append(pos, ph)
		}
	}
	if len(pos) == 0 {
		return ""
	}
	if last := pos[len(pos)-1]; !strings.Contains(last, " ") {
		pos[len(pos)-1] = last + "*"
	}
	out := strings.Join(pos, " AND ")
	for _, n := range neg {
		out += " NOT " + n
	}
	return out
}

func splitWords(q string) []string {
	var out []string
	var cur strings.Builder
	inQ := false
	for _, r := range q {
		switch {
		case r == '"':
			inQ = !inQ
			cur.WriteRune(r)
		case unicode.IsSpace(r) && !inQ:
			if cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

func (d *DB) where(f Filter, alias string) (string, []any) {
	var w []string
	var args []any
	col := func(c string) string { return alias + "." + c }
	if f.From > 0 {
		w, args = append(w, col("at")+" >= ?"), append(args, f.From)
	}
	if f.To > 0 {
		w, args = append(w, col("at")+" < ?"), append(args, f.To)
	}
	if f.MinLevel > 0 {
		w, args = append(w, col("level")+" >= ?"), append(args, f.MinLevel)
	}
	srcs := append([]int64{}, f.Sources...)
	if len(f.Groups) > 0 {
		gs := map[string]bool{}
		for _, g := range f.Groups {
			gs[g] = true
		}
		for _, s := range d.AllSources() {
			if gs[s.Group] {
				srcs = append(srcs, s.ID)
			}
		}
		if len(srcs) == 0 {
			srcs = []int64{-1}
		}
	}
	if len(srcs) > 0 {
		w = append(w, col("source_id")+" IN ("+placeholders(len(srcs))+")")
		for _, s := range srcs {
			args = append(args, s)
		}
	}
	if f.Pattern != 0 {
		w, args = append(w, col("pattern")+" = ?"), append(args, f.Pattern)
	}
	if f.Before > 0 {
		w, args = append(w, col("id")+" < ?"), append(args, f.Before)
	}
	if f.After > 0 {
		w, args = append(w, col("id")+" > ?"), append(args, f.After)
	}
	if len(w) == 0 {
		return "1", nil
	}
	return strings.Join(w, " AND "), args
}

// Lines lists lines, newest first, with whether there are more.
func (d *DB) Lines(ctx context.Context, f Filter) ([]Line, bool, error) {
	if f.Limit <= 0 || f.Limit > 1000 {
		f.Limit = 200
	}
	where, args := d.where(f, "l")
	var q string
	order := "l.id DESC"
	if f.After > 0 {
		order = "l.id ASC"
	}
	if fts := FTSQuery(f.Q); fts != "" {
		q = `SELECT l.id, l.at, l.source_id, l.level, l.stream, l.pattern, l.msg, l.attrs FROM lines_fts f JOIN lines l ON l.id = f.rowid
			WHERE lines_fts MATCH ? AND ` + where + ` ORDER BY f.rowid ` + strings.TrimPrefix(order, "l.id ")
		args = append([]any{fts}, args...)
	} else {
		q = `SELECT l.id, l.at, l.source_id, l.level, l.stream, l.pattern, l.msg, l.attrs FROM lines l WHERE ` + where + ` ORDER BY ` + order
	}
	q += ` LIMIT ` + strconv.Itoa(f.Limit+1)
	out, err := d.scanLines(ctx, q, args...)
	if err != nil {
		return nil, false, err
	}
	more := len(out) > f.Limit
	if more {
		out = out[:f.Limit]
	}
	if f.After > 0 { // newest first, like the rest
		for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
			out[i], out[j] = out[j], out[i]
		}
	}
	return out, more, nil
}

func (d *DB) scanLines(ctx context.Context, q string, args ...any) ([]Line, error) {
	rows, err := d.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Line{}
	for rows.Next() {
		var l Line
		var lv, stream int
		var pat int64
		var attrs sql.NullString
		if err := rows.Scan(&l.ID, &l.At, &l.Source, &lv, &stream, &pat, &l.Msg, &attrs); err != nil {
			return nil, err
		}
		l.Level = LevelNames[lv]
		l.Stream = map[int]string{1: "stdout", 2: "stderr"}[stream]
		l.Pattern = strconv.FormatInt(pat, 10)
		if attrs.Valid && attrs.String != "" {
			_ = json.Unmarshal([]byte(attrs.String), &l.Attrs)
		}
		if s := d.SourceByID(l.Source); s != nil {
			l.Name, l.Label, l.Group = s.Name, s.Label, s.Group
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// Line reads one line.
func (d *DB) Line(ctx context.Context, id int64) (*Line, error) {
	ls, err := d.scanLines(ctx, `SELECT id, at, source_id, level, stream, pattern, msg, attrs FROM lines WHERE id = ?`, id)
	if err != nil {
		return nil, err
	}
	if len(ls) == 0 {
		return nil, ErrNotFound
	}
	return &ls[0], nil
}

// Context is the lines of the same source around one (n before, n after),
// oldest first.
func (d *DB) Context(ctx context.Context, id int64, n int) ([]Line, error) {
	l, err := d.Line(ctx, id)
	if err != nil {
		return nil, err
	}
	if n <= 0 || n > 200 {
		n = 25
	}
	cols := `id, at, source_id, level, stream, pattern, msg, attrs`
	before, err := d.scanLines(ctx, `SELECT `+cols+` FROM lines WHERE source_id = ? AND (at < ? OR at = ? AND id < ?) ORDER BY at DESC, id DESC LIMIT ?`, l.Source, l.At, l.At, l.ID, n)
	if err != nil {
		return nil, err
	}
	after, err := d.scanLines(ctx, `SELECT `+cols+` FROM lines WHERE source_id = ? AND (at > ? OR at = ? AND id > ?) ORDER BY at, id LIMIT ?`, l.Source, l.At, l.At, l.ID, n)
	if err != nil {
		return nil, err
	}
	out := make([]Line, 0, len(before)+len(after)+1)
	for i := len(before) - 1; i >= 0; i-- {
		out = append(out, before[i])
	}
	out = append(out, *l)
	return append(out, after...), nil
}

// Around is a source's lines in a window around a time, oldest first.
func (d *DB) Around(ctx context.Context, source int64, at time.Time, span time.Duration, limit int) ([]Line, error) {
	if limit <= 0 || limit > 500 {
		limit = 120
	}
	ms := at.UnixMilli()
	half := span.Milliseconds() / 2
	cols := `id, at, source_id, level, stream, pattern, msg, attrs`
	before, err := d.scanLines(ctx, `SELECT `+cols+` FROM lines WHERE source_id = ? AND at >= ? AND at <= ? ORDER BY at DESC, id DESC LIMIT ?`, source, ms-half, ms, limit*2/3)
	if err != nil {
		return nil, err
	}
	after, err := d.scanLines(ctx, `SELECT `+cols+` FROM lines WHERE source_id = ? AND at > ? AND at <= ? ORDER BY at, id LIMIT ?`, source, ms, ms+half, limit/3)
	if err != nil {
		return nil, err
	}
	out := make([]Line, 0, len(before)+len(after))
	for i := len(before) - 1; i >= 0; i-- {
		out = append(out, before[i])
	}
	return append(out, after...), nil
}

// ---- counts over time ----

// Bucket picks the bar width for a span.
func Bucket(span time.Duration) time.Duration {
	switch {
	case span <= 90*time.Minute:
		return time.Minute
	case span <= 3*time.Hour:
		return 2 * time.Minute
	case span <= 7*time.Hour:
		return 5 * time.Minute
	case span <= 13*time.Hour:
		return 10 * time.Minute
	case span <= 26*time.Hour:
		return 15 * time.Minute
	case span <= 50*time.Hour:
		return 30 * time.Minute
	case span <= 8*24*time.Hour:
		return time.Hour
	case span <= 15*24*time.Hour:
		return 3 * time.Hour
	}
	return 6 * time.Hour
}

// Histogram is lines per bar and level for a filter (the kept lines).
type Histogram struct {
	From   int64            `json:"from"`
	To     int64            `json:"to"`
	Bucket int64            `json:"bucket"` // ms
	Levels map[string][]int `json:"levels"` // level word → count per bar
	Total  int              `json:"total"`
}

func newHist(from, to int64, bucket time.Duration) *Histogram {
	b := bucket.Milliseconds()
	from = from - from%b
	n := int((to - from + b - 1) / b)
	if n < 1 {
		n = 1
	}
	h := &Histogram{From: from, To: from + int64(n)*b, Bucket: b, Levels: map[string][]int{}}
	for _, w := range LevelNames {
		h.Levels[w] = make([]int, n)
	}
	return h
}

func (h *Histogram) add(at int64, lv, n int) {
	i := int((at - h.From) / h.Bucket)
	w := LevelNames[lv]
	if w == "" || i < 0 || i >= len(h.Levels[w]) {
		return
	}
	h.Levels[w][i] += n
	h.Total += n
}

// LineHistogram counts the kept lines that match a filter (From and To needed).
func (d *DB) LineHistogram(ctx context.Context, f Filter) (*Histogram, error) {
	h := newHist(f.From, f.To, Bucket(time.Duration(f.To-f.From)*time.Millisecond))
	f.From = h.From
	f.Before, f.After = 0, 0
	where, args := d.where(f, "l")
	bexpr := `(l.at - ?) / ?`
	args = append([]any{h.From, h.Bucket}, args...)
	var q string
	if fts := FTSQuery(f.Q); fts != "" {
		q = `SELECT ` + bexpr + ` AS k, l.level, COUNT(1) FROM lines_fts f JOIN lines l ON l.id = f.rowid WHERE lines_fts MATCH ? AND ` + where + ` GROUP BY k, l.level`
		args = append(args[:2], append([]any{fts}, args[2:]...)...)
	} else {
		q = `SELECT ` + bexpr + ` AS k, l.level, COUNT(1) FROM lines l WHERE ` + where + ` GROUP BY k, l.level`
	}
	rows, err := d.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var k int64
		var lv, n int
		if err := rows.Scan(&k, &lv, &n); err != nil {
			return nil, err
		}
		h.add(h.From+k*h.Bucket, lv, n)
	}
	return h, rows.Err()
}

// Overview is the counts of every line written (kept or not) for a span.
type Overview struct {
	Volume   *Histogram       `json:"volume"`
	Totals   map[string]int   `json:"totals"` // by level, and "all"
	Previous map[string]int   `json:"previous"`
	Bytes    int64            `json:"bytes"`
	Sources  []SourceCount    `json:"sources"`
	Heat     Heat             `json:"heat"`
	Groups   map[string][]int `json:"groups"` // lines per bar per group
}

// SourceCount is one source's lines in a span.
type SourceCount struct {
	Source
	Lines    int   `json:"lines"`
	Errors   int   `json:"errors"` // error and fatal
	Warnings int   `json:"warnings"`
	Bytes    int64 `json:"bytes"`
	Spark    []int `json:"spark"` // per bar
}

// Heat is errors (and lines) per source per bar, for the busiest sources.
type Heat struct {
	Sources []int64  `json:"sources"`
	Labels  []string `json:"labels"`
	Bucket  int64    `json:"bucket"`
	From    int64    `json:"from"`
	Lines   [][]int  `json:"lines"`
	Errors  [][]int  `json:"errors"`
}

// Overview reads the counts for [from, to) (unix ms).
func (d *DB) Overview(ctx context.Context, from, to int64) (*Overview, error) {
	span := time.Duration(to-from) * time.Millisecond
	bucket := Bucket(span)
	table, base := "roll5", int64(300)
	if bucket >= time.Hour || from < time.Now().Add(-70*time.Hour).UnixMilli() {
		table, base = "roll60", 3600
		if bucket < time.Hour {
			bucket = time.Hour
		}
	}
	if bucket < time.Duration(base)*time.Second {
		bucket = time.Duration(base) * time.Second
	}
	h := newHist(from, to, bucket)
	o := &Overview{Volume: h, Totals: map[string]int{}, Previous: map[string]int{}, Groups: map[string][]int{}}
	bsec := h.Bucket / 1000
	fromS, toS := h.From/1000, (to+999)/1000
	rows, err := d.db.QueryContext(ctx, `SELECT b, source_id, level, n, bytes FROM `+table+` WHERE b >= ? AND b < ?`, fromS-fromS%base, toS)
	if err != nil {
		return nil, err
	}
	type acc struct {
		sc    *SourceCount
		bars  []int
		ebars []int
	}
	per := map[int64]*acc{}
	nb := len(h.Levels["info"])
	for rows.Next() {
		var b, src, bytes int64
		var lv, n int
		if err := rows.Scan(&b, &src, &lv, &n, &bytes); err != nil {
			rows.Close()
			return nil, err
		}
		h.add(b*1000, lv, n)
		o.Totals[LevelNames[lv]] += n
		o.Totals["all"] += n
		o.Bytes += bytes
		a := per[src]
		if a == nil {
			a = &acc{sc: &SourceCount{}, bars: make([]int, nb), ebars: make([]int, nb)}
			per[src] = a
		}
		a.sc.Lines += n
		a.sc.Bytes += bytes
		if lv >= Error {
			a.sc.Errors += n
		} else if lv == Warn {
			a.sc.Warnings += n
		}
		if i := int((b - h.From/1000) / bsec); i >= 0 && i < nb {
			a.bars[i] += n
			if lv >= Error {
				a.ebars[i] += n
			}
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// the span before, to compare
	pf, pt := 2*h.From/1000-toS, h.From/1000
	prow, err := d.db.QueryContext(ctx, `SELECT level, SUM(n) FROM `+table+` WHERE b >= ? AND b < ? GROUP BY level`, pf, pt)
	if err == nil {
		for prow.Next() {
			var lv, n int
			if prow.Scan(&lv, &n) == nil {
				o.Previous[LevelNames[lv]] += n
				o.Previous["all"] += n
			}
		}
		prow.Close()
	}
	for src, a := range per {
		s := d.SourceByID(src)
		if s == nil {
			continue
		}
		a.sc.Source = *s
		a.sc.Spark = a.bars
		o.Sources = append(o.Sources, *a.sc)
		g := o.Groups[s.Group]
		if g == nil {
			g = make([]int, nb)
			o.Groups[s.Group] = g
		}
		for i, v := range a.bars {
			g[i] += v
		}
	}
	sort.Slice(o.Sources, func(i, j int) bool { return o.Sources[i].Lines > o.Sources[j].Lines })
	// the heat map: the sources with the most errors, then the busiest
	hs := append([]SourceCount{}, o.Sources...)
	sort.SliceStable(hs, func(i, j int) bool {
		if hs[i].Errors != hs[j].Errors {
			return hs[i].Errors > hs[j].Errors
		}
		return hs[i].Lines > hs[j].Lines
	})
	if len(hs) > 14 {
		hs = hs[:14]
	}
	o.Heat = Heat{Bucket: h.Bucket, From: h.From, Lines: [][]int{}, Errors: [][]int{}, Sources: []int64{}, Labels: []string{}}
	for _, s := range hs {
		a := per[s.ID]
		o.Heat.Sources = append(o.Heat.Sources, s.ID)
		o.Heat.Labels = append(o.Heat.Labels, s.Label)
		o.Heat.Lines = append(o.Heat.Lines, a.bars)
		o.Heat.Errors = append(o.Heat.Errors, a.ebars)
	}
	if o.Sources == nil {
		o.Sources = []SourceCount{}
	}
	return o, nil
}

// ---- patterns ----

// Pattern is a message that repeats.
type Pattern struct {
	ID       string  `json:"id"`
	Source   int64   `json:"source_id"`
	Label    string  `json:"source_label"`
	Level    string  `json:"level"`
	Template string  `json:"template"`
	Sample   string  `json:"sample"`
	N        int     `json:"n"`     // in the span
	Total    int     `json:"total"` // ever (since it was first seen, within 30 days)
	FirstAt  int64   `json:"first_at"`
	LastAt   int64   `json:"last_at"`
	New      bool    `json:"new"` // first seen in the span
	Share    float64 `json:"share"`
	Spark    []int   `json:"spark"` // per hour of the span (at most 48)
}

// Patterns lists the messages that repeat most in [from, to): sort = top
// (most lines), new (first seen in the span, most first), or errors (only
// warnings and worse).
func (d *DB) Patterns(ctx context.Context, from, to int64, source int64, sortBy string, limit int) ([]Pattern, int, error) {
	if limit <= 0 || limit > 200 {
		limit = 30
	}
	fromH := from / 1000
	fromH -= fromH % 3600
	toS := (to + 999) / 1000
	q := `SELECT h.pattern, SUM(h.n) AS c FROM pattern60 h JOIN patterns p ON p.id = h.pattern WHERE h.b >= ? AND h.b < ?`
	args := []any{fromH, toS}
	if source > 0 {
		q += ` AND p.source_id = ?`
		args = append(args, source)
	}
	switch sortBy {
	case "new":
		q += ` AND p.first_at >= ?`
		args = append(args, from)
	case "errors":
		q += ` AND p.level >= 3`
	}
	q += ` GROUP BY h.pattern ORDER BY c DESC LIMIT ?`
	args = append(args, limit)
	rows, err := d.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, 0, err
	}
	type pc struct {
		id int64
		n  int
	}
	var ids []pc
	for rows.Next() {
		var x pc
		if rows.Scan(&x.id, &x.n) == nil {
			ids = append(ids, x)
		}
	}
	rows.Close()
	var all int
	_ = d.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(n),0) FROM pattern60 WHERE b >= ? AND b < ?`, fromH, toS).Scan(&all)
	hours := int((toS - fromH + 3599) / 3600)
	step := int64(3600)
	for hours > 48 {
		step *= 2
		hours = int((toS - fromH + step - 1) / step)
	}
	out := []Pattern{}
	for _, x := range ids {
		p := Pattern{ID: strconv.FormatInt(x.id, 10), N: x.n, Spark: make([]int, hours)}
		var lv int
		if err := d.db.QueryRowContext(ctx, `SELECT source_id, level, template, sample, n, first_at, last_at FROM patterns WHERE id = ?`, x.id).
			Scan(&p.Source, &lv, &p.Template, &p.Sample, &p.Total, &p.FirstAt, &p.LastAt); err != nil {
			continue
		}
		p.Level = LevelNames[lv]
		p.New = p.FirstAt >= from
		if all > 0 {
			p.Share = float64(p.N) / float64(all)
		}
		if s := d.SourceByID(p.Source); s != nil {
			p.Label = s.Label
		}
		hr, err := d.db.QueryContext(ctx, `SELECT b, n FROM pattern60 WHERE pattern = ? AND b >= ? AND b < ?`, x.id, fromH, toS)
		if err == nil {
			for hr.Next() {
				var b int64
				var n int
				if hr.Scan(&b, &n) == nil {
					if i := int((b - fromH) / step); i >= 0 && i < hours {
						p.Spark[i] += n
					}
				}
			}
			hr.Close()
		}
		out = append(out, p)
	}
	return out, all, nil
}

// ---- web traffic ----

// Web is the requests the proxy logged in a span.
type Web struct {
	From    int64            `json:"from"`
	Bucket  int64            `json:"bucket"`
	Classes map[string][]int `json:"classes"` // "2xx" … "5xx" → per bar
	Ms      []float64        `json:"ms"`      // the average answer time per bar
	Sites   []WebSite        `json:"sites"`
	Total   int              `json:"total"`
	Errors  int              `json:"errors"` // 5xx
	Client  int              `json:"client"` // 4xx
	Slow    int              `json:"slow"`   // over a second
	AvgMs   float64          `json:"avg_ms"`
}

// WebSite is one host's requests.
type WebSite struct {
	Host   string  `json:"host"`
	N      int     `json:"n"`
	C2     int     `json:"ok"`
	C3     int     `json:"redirects"`
	C4     int     `json:"client_errors"`
	C5     int     `json:"server_errors"`
	Slow   int     `json:"slow"`
	AvgMs  float64 `json:"avg_ms"`
	Spark  []int   `json:"spark"`
	ESpark []int   `json:"error_spark"`
}

// WebTraffic reads the web counts for [from, to).
func (d *DB) WebTraffic(ctx context.Context, from, to int64) (*Web, error) {
	bucket := Bucket(time.Duration(to-from) * time.Millisecond)
	table, base := "web5", int64(300)
	if bucket >= time.Hour || from < time.Now().Add(-70*time.Hour).UnixMilli() {
		table, base = "web60", 3600
	}
	if bucket < time.Duration(base)*time.Second {
		bucket = time.Duration(base) * time.Second
	}
	b := bucket.Milliseconds()
	f := from - from%b
	n := int((to - f + b - 1) / b)
	w := &Web{From: f, Bucket: b, Classes: map[string][]int{}, Ms: make([]float64, n), Sites: []WebSite{}}
	for _, c := range []string{"2xx", "3xx", "4xx", "5xx"} {
		w.Classes[c] = make([]int, n)
	}
	cnt := make([]int, n)
	rows, err := d.db.QueryContext(ctx, `SELECT b, host, class, n, ms, slow FROM `+table+` WHERE b >= ? AND b < ?`, (f/1000)-(f/1000)%base, (to+999)/1000)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	sites := map[string]*WebSite{}
	var msAll float64
	for rows.Next() {
		var bs int64
		var host string
		var class, c, slow int
		var ms float64
		if err := rows.Scan(&bs, &host, &class, &c, &ms, &slow); err != nil {
			return nil, err
		}
		i := int((bs*1000 - f) / b)
		if i < 0 || i >= n {
			continue
		}
		key := strconv.Itoa(class) + "xx"
		if v, ok := w.Classes[key]; ok {
			v[i] += c
		}
		w.Ms[i] += ms
		cnt[i] += c
		w.Total += c
		w.Slow += slow
		msAll += ms
		s := sites[host]
		if s == nil {
			s = &WebSite{Host: host, Spark: make([]int, n), ESpark: make([]int, n)}
			sites[host] = s
		}
		s.N += c
		s.Slow += slow
		s.AvgMs += ms
		s.Spark[i] += c
		switch class {
		case 2:
			s.C2 += c
		case 3:
			s.C3 += c
		case 4:
			s.C4 += c
			w.Client += c
		case 5:
			s.C5 += c
			w.Errors += c
			s.ESpark[i] += c
		}
	}
	for i := range w.Ms {
		if cnt[i] > 0 {
			w.Ms[i] = round1(w.Ms[i] / float64(cnt[i]))
		}
	}
	if w.Total > 0 {
		w.AvgMs = round1(msAll / float64(w.Total))
	}
	for _, s := range sites {
		if s.N > 0 {
			s.AvgMs = round1(s.AvgMs / float64(s.N))
		}
		w.Sites = append(w.Sites, *s)
	}
	sort.Slice(w.Sites, func(i, j int) bool { return w.Sites[i].N > w.Sites[j].N })
	return w, rows.Err()
}

func round1(f float64) float64 { return float64(int64(f*10+0.5)) / 10 }

package logs

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Journal follows the box's systemd journal (system and user services,
// the kernel) through journalctl reading the journal files mounted into
// the container (read-only). Lines from containers that log to the
// journal are skipped: the Docker reader has them.
type Journal struct {
	Dir string // the journal directory, e.g. /host/journal

	mu      sync.Mutex
	running bool
	err     string
	lastAt  time.Time
	lines   int64
}

// JournalStatus is how the journal reading goes.
type JournalStatus struct {
	Available bool   `json:"available"` // the files are there
	Running   bool   `json:"running"`
	Error     string `json:"error"`
	LastAt    string `json:"last_at"`
	Lines     int64  `json:"lines"`
	Dir       string `json:"dir"`
}

// Available reports whether journalctl and the journal files are here.
func (j *Journal) Available() bool {
	if j == nil || j.Dir == "" {
		return false
	}
	if _, err := exec.LookPath("journalctl"); err != nil {
		return false
	}
	fi, err := os.Stat(j.Dir)
	return err == nil && fi.IsDir()
}

// Status is a copy of how it goes.
func (j *Journal) Status() JournalStatus {
	st := JournalStatus{Available: j.Available(), Dir: j.Dir}
	if j == nil {
		return st
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	st.Running, st.Error, st.Lines = j.running, j.err, j.lines
	if !j.lastAt.IsZero() {
		st.LastAt = j.lastAt.UTC().Format(time.RFC3339)
	}
	if !st.Available && st.Error == "" {
		if _, err := exec.LookPath("journalctl"); err != nil {
			st.Error = "journalctl is not in this image"
		} else {
			st.Error = "the journal is not mounted: add /var/log/journal:" + j.Dir + ":ro to the container"
		}
	}
	return st
}

// JournalEntry is one record as journalctl writes it in JSON.
type JournalEntry struct {
	Cursor   string
	At       time.Time
	Priority int
	Unit     string
	UserUnit string
	Ident    string
	Comm     string
	Message  string
	Kernel   bool
}

// Run follows the journal until ctx ends, handing each record to emit and
// the cursor to save every few seconds. It starts after cursor (or, with
// none, at the end), and never further back than an hour.
func (j *Journal) Run(ctx context.Context, cursor string, emit func([]JournalEntry), save func(string)) {
	backoff := 5 * time.Second
	for ctx.Err() == nil {
		if !j.Available() {
			j.set(false, "")
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Minute):
			}
			continue
		}
		next, err := j.follow(ctx, cursor, emit, save)
		if next != "" {
			cursor = next
		}
		if ctx.Err() != nil {
			return
		}
		msg := "journalctl stopped"
		if err != nil {
			msg = err.Error()
		}
		j.set(false, msg)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 5*time.Minute)
	}
}

func (j *Journal) set(running bool, err string) {
	j.mu.Lock()
	j.running, j.err = running, err
	j.mu.Unlock()
}

func (j *Journal) follow(ctx context.Context, cursor string, emit func([]JournalEntry), save func(string)) (string, error) {
	args := []string{"-D", j.Dir, "-o", "json", "--follow", "--no-pager", "--all", "--output-fields=MESSAGE,PRIORITY,_SYSTEMD_UNIT,_SYSTEMD_USER_UNIT,SYSLOG_IDENTIFIER,_COMM,_TRANSPORT,CONTAINER_NAME,CONTAINER_ID"}
	if cursor != "" {
		args = append(args, "--after-cursor="+cursor, "--since=-1h")
	} else {
		args = append(args, "--lines=0")
	}
	cmd := exec.CommandContext(ctx, "journalctl", args...)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	var stderr strings.Builder
	cmd.Stderr = &limitWriter{w: &stderr, n: 2000}
	if err := cmd.Start(); err != nil {
		return "", err
	}
	j.set(true, "")
	recs := make(chan JournalEntry, 4096)
	done := make(chan struct{})
	last := cursor
	go func() {
		defer close(done)
		t := time.NewTicker(2 * time.Second)
		defer t.Stop()
		var batch []JournalEntry
		flush := func() {
			if len(batch) == 0 {
				return
			}
			emit(batch)
			last = batch[len(batch)-1].Cursor
			save(last)
			j.mu.Lock()
			j.lines += int64(len(batch))
			j.lastAt = batch[len(batch)-1].At
			j.mu.Unlock()
			batch = nil
		}
		for {
			select {
			case r, ok := <-recs:
				if !ok {
					flush()
					return
				}
				batch = append(batch, r)
				if len(batch) >= 2000 {
					flush()
				}
			case <-t.C:
				flush()
			}
		}
	}()
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		if r, ok := parseJournal(sc.Bytes()); ok {
			recs <- r
		}
	}
	close(recs)
	<-done
	err = cmd.Wait()
	if ctx.Err() != nil {
		return last, nil
	}
	if s := strings.TrimSpace(stderr.String()); s != "" {
		return last, errors.New("journalctl: " + s)
	}
	if err != nil {
		return last, fmt.Errorf("journalctl: %w", err)
	}
	return last, nil
}

func parseJournal(b []byte) (JournalEntry, bool) {
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		return JournalEntry{}, false
	}
	if m["CONTAINER_NAME"] != nil || m["CONTAINER_ID"] != nil {
		return JournalEntry{}, false // a container logging to the journal: Docker gives it
	}
	r := JournalEntry{Cursor: str(m["__CURSOR"]), Unit: str(m["_SYSTEMD_UNIT"]), UserUnit: str(m["_SYSTEMD_USER_UNIT"]),
		Ident: str(m["SYSLOG_IDENTIFIER"]), Comm: str(m["_COMM"]), Kernel: str(m["_TRANSPORT"]) == "kernel"}
	if us, err := strconv.ParseInt(str(m["__REALTIME_TIMESTAMP"]), 10, 64); err == nil {
		r.At = time.UnixMicro(us)
	} else {
		r.At = time.Now()
	}
	r.Priority = 6
	if p, err := strconv.Atoi(str(m["PRIORITY"])); err == nil {
		r.Priority = p
	}
	switch v := m["MESSAGE"].(type) {
	case string:
		r.Message = v
	case []any: // not UTF-8: bytes
		bs := make([]byte, 0, len(v))
		for _, x := range v {
			if f, ok := x.(float64); ok {
				bs = append(bs, byte(f))
			}
		}
		r.Message = strings.ToValidUTF8(string(bs), "�")
	}
	return r, r.Cursor != ""
}

// Source names the source of a journal record: the user's own services as
// "user:<unit>", system services by unit, the kernel, else the program.
func (r JournalEntry) Source() SourceInfo {
	label := func(u string) string { return strings.TrimSuffix(strings.TrimSuffix(u, ".service"), ".scope") }
	switch {
	case r.Kernel:
		return SourceInfo{Kind: "journal", Name: "kernel", Label: "kernel", Group: "kernel"}
	case r.UserUnit == "init.scope":
		return SourceInfo{Kind: "journal", Name: "user:systemd", Label: "systemd (user)", Group: "user"}
	case r.UserUnit != "" && !strings.HasPrefix(r.UserUnit, "session-") && !strings.HasPrefix(r.UserUnit, "app-") && !strings.HasPrefix(r.UserUnit, "run-"):
		return SourceInfo{Kind: "journal", Name: "user:" + r.UserUnit, Label: label(r.UserUnit), Group: "user"}
	case r.Unit != "" && !strings.HasPrefix(r.Unit, "session-") && !strings.HasPrefix(r.Unit, "run-") && r.Unit != "init.scope":
		if strings.HasPrefix(r.Unit, "user@") && r.Ident != "" {
			return SourceInfo{Kind: "journal", Name: "user:" + r.Ident, Label: r.Ident, Group: "user"}
		}
		return SourceInfo{Kind: "journal", Name: r.Unit, Label: label(r.Unit), Group: "system"}
	}
	name := firstOf(r.Ident, r.Comm, "journal")
	return SourceInfo{Kind: "journal", Name: "prog:" + name, Label: name, Group: "system"}
}

// Level reads the line for its own level word first (Go and Python
// services often log everything at one priority), else the priority.
func (r JournalEntry) Entry() Entry {
	e := Parse(r.Message, 0)
	byPrio := map[int]int{0: Fatal, 1: Fatal, 2: Fatal, 3: Error, 4: Warn, 5: Info, 6: Info, 7: Debug}[r.Priority]
	if e.Level == Info && byPrio != Info { // nothing in the line said otherwise
		e.Level = byPrio
	}
	if e.Level == 0 {
		e.Level = Info
	}
	e.At = r.At
	return e
}

type limitWriter struct {
	w interface{ WriteString(string) (int, error) }
	n int
}

func (l *limitWriter) Write(p []byte) (int, error) {
	if l.n > 0 {
		s := string(p)
		if len(s) > l.n {
			s = s[:l.n]
		}
		l.n -= len(s)
		_, _ = l.w.WriteString(s)
	}
	return len(p), nil
}

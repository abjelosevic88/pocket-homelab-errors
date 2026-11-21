package server

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// fakeDocker serves follow streams over a unix socket: lines pushed to a
// container go out on its open stream, framed the way Docker does.
type fakeDocker struct {
	mu     sync.Mutex
	opens  int
	sinces []string
	feed   chan string
	closeC chan struct{}
}

func (f *fakeDocker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.opens++
	f.sinces = append(f.sinces, r.URL.Query().Get("since"))
	closeC := f.closeC
	f.mu.Unlock()
	if r.URL.Query().Get("follow") != "1" {
		w.WriteHeader(200)
		return
	}
	w.WriteHeader(200)
	w.(http.Flusher).Flush()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-closeC:
			return
		case line := <-f.feed:
			b := []byte(line + "\n")
			hdr := make([]byte, 8)
			hdr[0] = 1
			binary.BigEndian.PutUint32(hdr[4:], uint32(len(b)))
			w.Write(hdr)
			w.Write(b)
			w.(http.Flusher).Flush()
		}
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestFollowersStreamLines(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "d.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	fd := &fakeDocker{feed: make(chan string, 10), closeC: make(chan struct{})}
	srv := &http.Server{Handler: fd}
	go srv.Serve(ln)
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fs := newFollowers(ctx, newDockerClient(sock))
	t0 := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	stamp := func(s int) string { return t0.Add(time.Duration(s) * time.Second).Format(time.RFC3339Nano) }

	// first read opens the stream from since and has nothing yet
	lines, ok := fs.read("c1", t0)
	if !ok || len(lines) != 0 {
		t.Fatalf("first read = %v %v", lines, ok)
	}
	fd.feed <- stamp(1) + " one"
	fd.feed <- stamp(2) + " two"
	waitFor(t, "two lines", func() bool { l, _ := fs.read("c1", t0); return len(l) == 2 })

	// a reader that already has line one gets only line two
	lines, _ = fs.read("c1", t0.Add(time.Second))
	if len(lines) != 1 || lines[0].text != "two" {
		t.Fatalf("after one = %+v", lines)
	}
	// a reader from before the stream started is not vouched for
	if _, ok := fs.read("c1", t0.Add(-time.Minute)); ok {
		t.Fatal("reader behind the buffer should fall back to Docker")
	}

	// the stream ends (container restarted): reopened from its last line,
	// the repeated line is dropped and no line is lost
	fd.mu.Lock()
	close(fd.closeC)
	fd.closeC = make(chan struct{})
	fd.mu.Unlock()
	waitFor(t, "stream to end", func() bool { return fs.count() == 0 })
	fs.read("c1", t0.Add(2*time.Second))
	fd.feed <- stamp(2) + " two"
	fd.feed <- stamp(3) + " three"
	waitFor(t, "three lines", func() bool { l, _ := fs.read("c1", t0); return len(l) == 3 })
	fd.mu.Lock()
	opens, sinces := fd.opens, append([]string{}, fd.sinces...)
	fd.mu.Unlock()
	if opens != 2 || sinces[1] != sinceArg(t0.Add(2*time.Second)) {
		t.Fatalf("opens=%d sinces=%v", opens, sinces)
	}

	// nobody reads it: closed and forgotten
	fs.mu.Lock()
	fs.m["c1"].used = time.Now().Add(-time.Hour)
	fs.mu.Unlock()
	fs.tidy()
	waitFor(t, "idle stream closed", func() bool { return fs.count() == 0 })
	fs.mu.Lock()
	n := len(fs.m)
	fs.mu.Unlock()
	if n != 0 {
		t.Fatal(fmt.Sprint("followers left: ", n))
	}
}

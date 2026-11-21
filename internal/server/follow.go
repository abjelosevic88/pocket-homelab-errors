package server

import (
	"context"
	"sync"
	"time"
)

// ---- following containers' logs ----
//
// Asking Docker every tick for "the lines since T" makes it read each
// container's log file from the top to find T: with a hundred-odd
// containers every ten seconds that kept dockerd at a third of a core.
// Instead Errors keeps one open follow stream per container it reads;
// Docker pushes each new line once, and the logs and the error watcher take
// theirs from a short buffer here. A stream that ends (the container
// stopped or restarted, Docker restarted) is opened again from its last
// line on the next read; one nobody read for a while is closed.

const (
	followKeep    = 3 * time.Minute // lines stay this long for readers that are behind
	followMax     = 20000           // lines per container at most
	followIdle    = 2 * time.Minute // a stream nobody read for this long is closed
	followRetry   = 30 * time.Second
	followBackoff = 5 * time.Minute
)

type followers struct {
	docker *dockerClient
	ctx    context.Context

	mu sync.Mutex
	m  map[string]*follower // container id → its stream
}

type follower struct {
	mu      sync.Mutex
	lines   []followLine
	covered time.Time // lines after this are all in the buffer (while running)
	last    time.Time // the newest line seen, where a reopened stream starts
	running bool
	err     error
	failed  int       // opens that failed in a row
	retryAt time.Time // no new open before this after a failure
	used    time.Time
	cancel  context.CancelFunc
}

type followLine struct {
	logLine
	got time.Time
}

func newFollowers(ctx context.Context, d *dockerClient) *followers {
	return &followers{docker: d, ctx: ctx, m: map[string]*follower{}}
}

// read returns a container's lines after since. ok is false when the
// buffer cannot vouch for everything after since (a reader further behind
// than the buffer, or a stream that cannot be opened): the caller reads
// Docker directly then, as before.
func (fs *followers) read(id string, since time.Time) (lines []logLine, ok bool) {
	if fs == nil {
		return nil, false
	}
	fs.mu.Lock()
	f := fs.m[id]
	if f == nil {
		f = &follower{covered: since, last: since}
		fs.m[id] = f
	}
	fs.mu.Unlock()

	now := time.Now()
	f.mu.Lock()
	defer f.mu.Unlock()
	f.used = now
	if !f.running {
		if now.Before(f.retryAt) {
			return nil, false
		}
		// Everything up to the newest line seen is in the buffer already
		// (a stream hands lines over in order), so a reopened stream starts
		// there and what is covered stays as it was.
		f.start(fs, id, f.last)
	}
	if since.Before(f.covered) {
		return nil, false
	}
	for _, l := range f.lines {
		if l.at.After(since) {
			lines = append(lines, l.logLine)
		}
	}
	return lines, true
}

// start opens the stream in the background. f.mu is held.
func (f *follower) start(fs *followers, id string, since time.Time) {
	ctx, cancel := context.WithCancel(fs.ctx)
	f.cancel, f.running = cancel, true
	go func() {
		defer cancel()
		body, err := fs.docker.follow(ctx, id, sinceArg(since))
		if err != nil {
			f.mu.Lock()
			f.running, f.err = false, err
			f.failed++
			wait := followRetry * time.Duration(f.failed)
			if wait > followBackoff {
				wait = followBackoff
			}
			f.retryAt = time.Now().Add(wait)
			f.mu.Unlock()
			return
		}
		f.mu.Lock()
		f.failed, f.err = 0, nil
		f.mu.Unlock()
		stop := context.AfterFunc(ctx, func() { body.Close() })
		defer stop()
		defer body.Close()
		_ = scanLogStream(body, func(l logLine) {
			f.mu.Lock()
			defer f.mu.Unlock()
			// Docker's since is inclusive: a reopened stream starts with the
			// line it ended on
			if n := len(f.lines); n > 0 && f.lines[n-1].stamp == l.stamp && f.lines[n-1].text == l.text {
				return
			}
			f.lines = append(f.lines, followLine{logLine: l, got: time.Now()})
			if l.at.After(f.last) {
				f.last = l.at
			}
			f.trim()
		})
		f.mu.Lock()
		f.running = false
		f.mu.Unlock()
	}()
}

// trim drops lines past their keep time or over the cap. f.mu is held.
func (f *follower) trim() {
	cut := time.Now().Add(-followKeep)
	n := 0
	for n < len(f.lines) && (f.lines[n].got.Before(cut) || len(f.lines)-n > followMax) {
		n++
	}
	if n == 0 {
		return
	}
	if at := f.lines[n-1].at; at.After(f.covered) {
		f.covered = at
	}
	f.lines = f.lines[n:]
}

// tidy closes streams nobody read lately (containers gone, switched off or
// archived) and trims quiet buffers. Called once a tick.
func (fs *followers) tidy() {
	if fs == nil {
		return
	}
	now := time.Now()
	fs.mu.Lock()
	defer fs.mu.Unlock()
	for id, f := range fs.m {
		f.mu.Lock()
		idle := now.Sub(f.used) > followIdle
		if idle && f.cancel != nil {
			f.cancel()
		}
		if !idle {
			f.trim()
		}
		f.mu.Unlock()
		if idle {
			delete(fs.m, id)
		}
	}
}

// count is how many streams are open, for the status.
func (fs *followers) count() int {
	if fs == nil {
		return 0
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()
	n := 0
	for _, f := range fs.m {
		f.mu.Lock()
		if f.running {
			n++
		}
		f.mu.Unlock()
	}
	return n
}

package server

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// dockerClient reads container lists and logs from the Docker Engine over
// its socket. Nothing else: Errors never starts, stops or changes anything.
type dockerClient struct {
	socket string
	http   *http.Client
}

func newDockerClient(socket string) *dockerClient {
	tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", socket)
	}}
	return &dockerClient{socket: socket, http: &http.Client{Transport: tr, Timeout: 30 * time.Second}}
}

// available says whether the socket is there (mounted into the container).
func (d *dockerClient) available() bool {
	if d == nil || d.socket == "" {
		return false
	}
	fi, err := os.Stat(d.socket)
	return err == nil && fi.Mode()&os.ModeSocket != 0
}

type container struct {
	ID    string
	Name  string
	Image string
	State string
}

func (d *dockerClient) get(ctx context.Context, path string, q url.Values) (*http.Response, error) {
	u := "http://docker" + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	res, err := d.http.Do(req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 2000))
		res.Body.Close()
		return nil, fmt.Errorf("docker: %s: %s", res.Status, strings.TrimSpace(string(b)))
	}
	return res, nil
}

// containers lists the running containers.
func (d *dockerClient) containers(ctx context.Context) ([]container, error) {
	res, err := d.get(ctx, "/containers/json", nil)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	var raw []struct {
		ID    string   `json:"Id"`
		Names []string `json:"Names"`
		Image string   `json:"Image"`
		State string   `json:"State"`
	}
	if err := json.NewDecoder(res.Body).Decode(&raw); err != nil {
		return nil, err
	}
	out := make([]container, 0, len(raw))
	for _, c := range raw {
		name := c.ID
		if len(c.Names) > 0 {
			name = strings.TrimPrefix(c.Names[0], "/")
		}
		out = append(out, container{ID: c.ID, Name: name, Image: c.Image, State: c.State})
	}
	return out, nil
}

// logLine is one line of a container's output with Docker's time for it.
type logLine struct {
	stamp string // as Docker wrote it, to ask for what comes after
	at    time.Time
	text  string
}

// logs reads a container's lines since a time (Docker's own stamp, or a
// unix time), up to max bytes.
func (d *dockerClient) logs(ctx context.Context, id string, since string, max int64) ([]logLine, error) {
	q := url.Values{"stdout": {"1"}, "stderr": {"1"}, "timestamps": {"1"}, "since": {since}}
	res, err := d.get(ctx, "/containers/"+url.PathEscape(id)+"/logs", q)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	body := io.LimitReader(res.Body, max)
	br := bufio.NewReaderSize(body, 64<<10)
	head, _ := br.Peek(8)
	var text io.Reader = br
	// without a terminal Docker multiplexes stdout and stderr in frames
	// (8-byte header: stream, 0, 0, 0, size)
	if len(head) == 8 && head[0] <= 2 && head[1] == 0 && head[2] == 0 && head[3] == 0 {
		pr, pw := io.Pipe()
		go func() {
			hdr := make([]byte, 8)
			for {
				if _, err := io.ReadFull(br, hdr); err != nil {
					pw.Close()
					return
				}
				n := int64(binary.BigEndian.Uint32(hdr[4:]))
				if _, err := io.CopyN(pw, br, n); err != nil {
					pw.CloseWithError(err)
					return
				}
			}
		}()
		defer pr.Close()
		text = pr
	}
	sc := bufio.NewScanner(text)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	var out []logLine
	for sc.Scan() {
		line := sc.Text()
		stamp, rest, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		at, err := time.Parse(time.RFC3339Nano, stamp)
		if err != nil {
			continue
		}
		out = append(out, logLine{stamp: stamp, at: at, text: rest})
	}
	return out, nil
}

// sinceArg turns Docker's stamp into the since= it takes (unix seconds with nanoseconds).
func sinceArg(t time.Time) string {
	return fmt.Sprintf("%d.%09d", t.Unix(), t.Nanosecond())
}

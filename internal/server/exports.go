package server

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/abjelosevic88/pocket-homelab-errors/internal/store"
)

// exportDoc is everything of one person: projects (with their keys, so
// apps keep sending after a move), issues with their hourly counts, kept
// events and links.
type exportDoc struct {
	App        string           `json:"app"`
	Version    string           `json:"version"`
	ExportedAt string           `json:"exported_at"`
	Projects   []*exportProject `json:"projects"`
}

type exportProject struct {
	*store.Project
	Issues []*exportIssue `json:"issues"`
}

type exportIssue struct {
	*store.Issue
	HourCounts map[string]int     `json:"hour_counts"`
	Events     []*store.Event     `json:"events"`
	LinksOut   []*store.IssueLink `json:"links"`
}

func (s *Server) export(ctx context.Context, uid int64) (exportDoc, error) {
	doc := exportDoc{App: "pocket-errors", Version: Version, ExportedAt: time.Now().UTC().Format(time.RFC3339), Projects: []*exportProject{}}
	projects, err := s.store.ListProjects(ctx, uid, true)
	if err != nil {
		return doc, err
	}
	links, err := s.store.ListLinks(ctx, uid, 0)
	if err != nil {
		return doc, err
	}
	byIssue := map[int64][]*store.IssueLink{}
	for _, l := range links {
		byIssue[l.IssueID] = append(byIssue[l.IssueID], l)
	}
	for _, p := range projects {
		p.Hours = nil
		ep := &exportProject{Project: p, Issues: []*exportIssue{}}
		list, _, err := s.store.ListIssues(ctx, uid, store.IssueFilter{Status: "all", ProjectID: p.ID, Limit: 500})
		if err != nil {
			return doc, err
		}
		for off := 500; len(list) == off; off += 500 {
			more, _, err := s.store.ListIssues(ctx, uid, store.IssueFilter{Status: "all", ProjectID: p.ID, Limit: 500, Offset: off})
			if err != nil {
				return doc, err
			}
			list = append(list, more...)
		}
		for _, is := range list {
			is.Hours = nil
			ei := &exportIssue{Issue: is, LinksOut: byIssue[is.ID]}
			if ei.HourCounts, err = s.store.AllHours(ctx, is.ID); err != nil {
				return doc, err
			}
			if ei.Events, err = s.store.ListEvents(ctx, uid, is.ID, 0, 200); err != nil {
				return doc, err
			}
			if ei.LinksOut == nil {
				ei.LinksOut = []*store.IssueLink{}
			}
			ep.Issues = append(ep.Issues, ei)
		}
		doc.Projects = append(doc.Projects, ep)
	}
	return doc, nil
}

func (s *Server) handleExportJSON(w http.ResponseWriter, r *http.Request) {
	doc, err := s.export(r.Context(), actorID(r.Context()))
	if err != nil {
		s.fail(w, err, "export")
		return
	}
	w.Header().Set("Content-Disposition", `attachment; filename="pocket-errors-export.json"`)
	writeJSON(w, http.StatusOK, doc)
}

// handleExportCSV is every issue in one table (a spreadsheet opens it).
func (s *Server) handleExportCSV(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	list, _, err := s.store.ListIssues(ctx, actorID(ctx), store.IssueFilter{Status: "all", Limit: 500})
	if err != nil {
		s.fail(w, err, "export")
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="pocket-errors-issues.csv"`)
	_, _ = w.Write([]byte("\ufeff"))
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"Project", "Issue", "Where", "Level", "Status", "Times", "People", "First seen", "Last seen", "First release", "Last release", "Link", "Note"})
	for _, is := range list {
		_ = cw.Write([]string{is.ProjectName, is.Title, is.Culprit, is.Level, is.Status, fmt.Sprint(is.Times), fmt.Sprint(is.People), is.FirstSeen, is.LastSeen,
			is.FirstRelease, is.LastRelease, s.link("/issues/" + itoa(is.ID)), is.Note})
	}
	cw.Flush()
}

// handleImport takes a Pocket Errors export (again and again: nothing is doubled).
func (s *Server) handleImport(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	uid := actorID(ctx)
	var data []byte
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
		r.Body = http.MaxBytesReader(w, r.Body, s.cfg.MaxBodyBytes)
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			writeErr(w, http.StatusBadRequest, "the file is too big")
			return
		}
		f, _, err := r.FormFile("file")
		if err != nil {
			writeErr(w, http.StatusBadRequest, "no file")
			return
		}
		defer f.Close()
		if data, err = io.ReadAll(f); err != nil {
			writeErr(w, http.StatusBadRequest, "could not read the file")
			return
		}
	} else {
		b, err := s.readBody(r)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "the file is too big")
			return
		}
		data = b
	}
	data = bytes.TrimPrefix(data, []byte("\ufeff"))
	var doc exportDoc
	if err := json.Unmarshal(data, &doc); err != nil || doc.App != "pocket-errors" {
		writeErr(w, http.StatusBadRequest, "that is not a Pocket Errors export")
		return
	}
	res, err := s.importDoc(ctx, uid, doc)
	if err != nil {
		s.fail(w, err, "import")
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// importDoc brings back an export. A project is found again by its key
// (or, watched containers and app projects, by what they watch); an issue
// by its fingerprint; an event by its id; a link by its app and ref.
func (s *Server) importDoc(ctx context.Context, uid int64, doc exportDoc) (map[string]any, error) {
	added := map[string]int{"projects": 0, "issues": 0, "events": 0, "links": 0}
	same := 0
	for _, ep := range doc.Projects {
		if ep == nil || ep.Project == nil {
			continue
		}
		in := ep.Project
		var p *store.Project
		if have, err := s.store.ProjectByKey(ctx, in.PublicKey); err == nil && have.UserID == uid {
			p = have
		} else if v, _, ok := s.store.CacheGet(ctx, "import:"+itoa(uid)+":"+in.PublicKey); ok && in.PublicKey != "" {
			n, _ := toInt64(v)
			p, _ = s.store.GetProject(ctx, uid, n)
		}
		switch {
		case p != nil:
		case in.Container != "":
			p, _ = s.store.ProjectByContainer(ctx, uid, in.Container)
		case in.Source != "" && in.Source != "docker":
			p, _ = s.store.ProjectBySource(ctx, uid, in.Source)
		}
		if p == nil {
			np := *in
			np.ID, np.UserID = 0, uid
			if !oneOf(np.Platform, store.Platforms) {
				np.Platform = "other"
			}
			if np.Name == "" {
				np.Name = "Imported project"
			}
			if have, err := s.store.ProjectByKey(ctx, np.PublicKey); err == nil && have.UserID != uid || np.PublicKey == "" {
				np.PublicKey = store.NewKey()
			}
			if err := s.store.CreateProject(ctx, &np); err != nil {
				return nil, err
			}
			if in.PublicKey != "" && np.PublicKey != in.PublicKey {
				_ = s.store.CachePut(ctx, "import:"+itoa(uid)+":"+in.PublicKey, itoa(np.ID))
			}
			p = &np
			added["projects"]++
		} else {
			same++
		}
		have, _ := s.store.ListLinks(ctx, uid, 0)
		linked := map[string]bool{}
		for _, l := range have {
			linked[fmt.Sprintf("%d|%s|%s", l.IssueID, l.App, l.Ref)] = true
		}
		for _, ei := range ep.Issues {
			if ei == nil || ei.Issue == nil || ei.Fingerprint == "" {
				continue
			}
			is, err := s.store.IssueByFingerprint(ctx, p.ID, ei.Fingerprint)
			if errors.Is(err, store.ErrNotFound) {
				is = ei.Issue
				if !oneOf(is.Status, []string{"open", "resolved", "ignored"}) {
					is.Status = "open"
				}
				if err := s.store.ImportIssue(ctx, p, is); err != nil {
					return nil, err
				}
				added["issues"]++
			} else if err != nil {
				return nil, err
			} else {
				same++
			}
			if err := s.store.AddHours(ctx, is, ei.HourCounts); err != nil {
				return nil, err
			}
			for _, e := range ei.Events {
				if e == nil {
					continue
				}
				ok, err := s.store.ImportEvent(ctx, is, e)
				if err != nil {
					return nil, err
				}
				if ok {
					added["events"]++
				}
			}
			for _, l := range ei.LinksOut {
				if l == nil || l.App == "" || linked[fmt.Sprintf("%d|%s|%s", is.ID, l.App, l.Ref)] {
					continue
				}
				nl := &store.IssueLink{IssueID: is.ID, App: l.App, Ref: l.Ref, Title: l.Title, URL: l.URL}
				if err := s.store.AddLink(ctx, uid, nl); err != nil {
					return nil, err
				}
				linked[fmt.Sprintf("%d|%s|%s", is.ID, l.App, l.Ref)] = true
				added["links"]++
			}
		}
	}
	return map[string]any{"added": added, "same": same}, nil
}

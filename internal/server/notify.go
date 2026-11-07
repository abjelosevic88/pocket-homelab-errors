package server

import (
	"context"
	"fmt"
	"time"

	"github.com/abjelosevic88/pocket-homelab-errors/internal/notify"
	"github.com/abjelosevic88/pocket-homelab-errors/internal/store"
)

// ---- telling the person ----
//
// A new issue, one that came back after it was resolved, and one happening
// a lot (the project's "per hour" mark, at most every six hours) are told
// — each kind only when the project has it on — through Pocket Alerts
// (alerts:write, so the person's own rules and quiet hours apply), a push
// to their own address, both or neither. Every one is also an event on
// Pocket Core's bus for Pocket Flows. At most 30 pushes an hour per person:
// a storm of errors is one look at the inbox, not a buzzing phone.

const toldPerHour = 30

func (s *Server) afterRecord(p *store.Project, res store.RecordResult) {
	is := res.Issue
	var kind string
	switch {
	case res.Outcome == store.OutcomeNew:
		kind = "new"
	case res.Outcome == store.OutcomeRegressed:
		kind = "regressed"
	case p.NotifySpike > 0 && res.HourN == p.NotifySpike && (is.SpikeAt == nil || olderThan(*is.SpikeAt, 6*time.Hour)):
		kind = "spike"
	default:
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if kind == "spike" {
			_ = s.store.MarkSpike(ctx, is.ID)
		}
		payload := map[string]any{"issue_id": is.ID, "project_id": p.ID, "project": p.Name, "title": is.Title, "culprit": is.Culprit,
			"level": is.Level, "times": is.Times, "url": s.link("/issues/" + itoa(is.ID)), "app": p.AppSlug}
		s.publish(ctx, p.UserID, "errors.issue_"+kind, itoa(is.ID), payload)
		if kind == "new" && !p.NotifyNew || kind == "regressed" && !p.NotifyAgain {
			return
		}
		if !s.mayTell(p.UserID) {
			return
		}
		title, body, prio := tellText(kind, p, is, res.HourN)
		s.tell(ctx, p.UserID, title, body, prio, s.link("/issues/"+itoa(is.ID)))
	}()
}

func olderThan(stamp string, d time.Duration) bool {
	t, err := time.Parse(time.RFC3339Nano, stamp)
	return err != nil || time.Since(t) > d
}

func tellText(kind string, p *store.Project, is *store.Issue, n int) (string, string, string) {
	where := is.Culprit
	if where != "" {
		where = "\n" + where
	}
	switch kind {
	case "regressed":
		return "Back in " + p.Name, is.Title + where + "\nYou had resolved it; it happened again.", "warning"
	case "spike":
		return fmt.Sprintf("%s: %d times this hour", p.Name, n), is.Title + where, "warning"
	}
	prio := "warning"
	if is.Level == "fatal" {
		prio = "failure"
	}
	return "New error in " + p.Name, is.Title + where, prio
}

// mayTell counts a push against the person's hour.
func (s *Server) mayTell(uid int64) bool {
	now := time.Now().Truncate(time.Hour)
	s.toldMu.Lock()
	defer s.toldMu.Unlock()
	c := s.told[uid]
	if c == nil || !c.at.Equal(now) {
		c = &minuteCount{at: now}
		s.told[uid] = c
	}
	c.n++
	return c.n <= toldPerHour
}

// tell sends one message the way the person chose; it says how it went.
func (s *Server) tell(ctx context.Context, uid int64, title, body, prio, link string) []string {
	st, _ := s.store.GetSettings(ctx, uid)
	via := st.NotifyVia
	var sent []string
	alerts := func() bool {
		c := s.clients(ctx, uid)
		sib, err := c.conn(ctx, appAlerts, scopeAlertsWrite)
		if err != nil {
			return false
		}
		p := map[string]int{"info": 3, "success": 3, "warning": 4, "failure": 5}[prio]
		if _, err := postJSONTo(ctx, sib, "/api/v1/alerts", map[string]any{"title": title, "body": body, "priority": p, "tags": []string{"errors"}, "click": link}); err != nil {
			s.log.Warn("telling through Pocket Alerts failed", "user", uid, "err", err)
			return false
		}
		sent = append(sent, "alerts")
		return true
	}
	push := func() bool {
		n := notify.New(st.NotifyURL, st.NotifyFormat)
		if !n.Configured() {
			return false
		}
		if err := n.Send(ctx, notify.Message{Title: title, Body: body, Priority: prio, Tags: []string{"rotating_light"}, Link: link}); err != nil {
			s.log.Warn("push failed", "user", uid, "err", err)
			return false
		}
		sent = append(sent, "push")
		return true
	}
	switch via {
	case "off":
	case "alerts":
		alerts()
	case "push":
		push()
	case "both":
		alerts()
		push()
	default: // auto: Pocket Alerts when it works, else the push
		if !alerts() {
			push()
		}
	}
	return sent
}

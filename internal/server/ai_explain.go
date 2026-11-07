package server

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/abjelosevic88/pocket-homelab-errors/internal/store"
)

// ---- Pocket AI (or the person's own OpenAI key) ----
//
// explain: what went wrong in one issue, the likely cause, where to look
// and how to fix it — from the issue and its newest event (the stack with
// the app's own lines, the request, what happened before, the log lines).
// Kept on the issue, so it is asked once. digest: a few sentences on what
// broke over a day or a week. Only when the person presses the button;
// nothing leaves for the AI otherwise. Secrets were taken out on the way in.

const explainSystem = "You help someone who runs their own apps understand an error their app had. " +
	"You get the error as Markdown: what it is, where, how often, the stack (→ marks the app's own code), the request, what happened before it and log lines. " +
	"Answer in Markdown with exactly these four short sections as bold labels: **What happened**, **Likely cause**, **Where to look** (name the files and functions from the stack), **How to fix** (concrete steps; a small code change when it is clear). " +
	"Plain words, no filler, under 250 words. When the information is not enough to know, say what to check instead of guessing. Never invent file names or functions that are not in the input."

func (s *Server) handleExplain(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	uid := actorID(ctx)
	st := s.settingsOf(ctx, uid)
	if !st.AIHelp {
		writeErr(w, http.StatusBadRequest, "AI help is switched off (Settings → AI)")
		return
	}
	md, is, p, err := s.issueMarkdown(ctx, uid, idParam(r, "id"), false)
	if err != nil {
		s.fail(w, err, "issue")
		return
	}
	var in struct {
		Again bool   `json:"again"`
		Hint  string `json:"hint"`
	}
	_ = decode(r, &in)
	if is.AIText != "" && !in.Again {
		writeJSON(w, http.StatusOK, map[string]any{"text": is.AIText, "at": is.AIAt})
		return
	}
	cl, _, why := s.aiFor(ctx, uid, st)
	if cl == nil {
		writeErr(w, http.StatusBadRequest, firstNonEmpty(why, "AI is not set up (Settings → AI)"))
		return
	}
	select {
	case s.aiSem <- struct{}{}:
		defer func() { <-s.aiSem }()
	default:
		writeErr(w, http.StatusTooManyRequests, "the AI is busy; try again in a moment")
		return
	}
	prompt := fmt.Sprintf("Platform: %s\n\n%s", firstNonEmpty(p.Platform, "unknown"), clip(md, 14000))
	if h := strings.TrimSpace(in.Hint); h != "" {
		prompt += "\n\nThe person adds: " + clip(h, 600)
	}
	out, err := cl.Complete(ctx, explainSystem, prompt, false)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "AI: "+err.Error())
		return
	}
	text := strings.TrimSpace(out)
	if err := s.store.SetAI(ctx, uid, is.ID, text); err != nil {
		s.fail(w, err, "save")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"text": text, "at": store.Now()})
}

// handleDigest is POST /ai/digest {range: 24h|7d}: a few sentences on what broke.
func (s *Server) handleDigest(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	uid := actorID(ctx)
	st := s.settingsOf(ctx, uid)
	if !st.AIHelp {
		writeErr(w, http.StatusBadRequest, "AI help is switched off (Settings → AI)")
		return
	}
	var in struct {
		Range string `json:"range"`
	}
	_ = decode(r, &in)
	d := 24 * time.Hour
	if in.Range == "7d" {
		d = 7 * 24 * time.Hour
	}
	list, _, err := s.store.ListIssues(ctx, uid, store.IssueFilter{Status: "all", Since: store.Stamp(time.Now().Add(-d)), Sort: "times", Limit: 40})
	if err != nil {
		s.fail(w, err, "digest")
		return
	}
	if len(list) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"text": "Nothing broke in this time."})
		return
	}
	cl, _, why := s.aiFor(ctx, uid, st)
	if cl == nil {
		writeErr(w, http.StatusBadRequest, firstNonEmpty(why, "AI is not set up (Settings → AI)"))
		return
	}
	select {
	case s.aiSem <- struct{}{}:
		defer func() { <-s.aiSem }()
	default:
		writeErr(w, http.StatusTooManyRequests, "the AI is busy; try again in a moment")
		return
	}
	since := store.Stamp(time.Now().Add(-d))
	var b strings.Builder
	for _, is := range list {
		mark := ""
		switch {
		case is.FirstSeen >= since:
			mark = " (new)"
		case is.Regressed && is.Status == "open":
			mark = " (came back)"
		}
		fmt.Fprintf(&b, "- %s — %s%s; %s; %d times in all, %d in the last 24 h; %s\n", is.ProjectName, is.Title, mark, firstNonEmpty(is.Culprit, "?"), is.Times, is.Day, is.Status)
	}
	sys := "You sum up the errors someone's apps had over a period, for the person who runs them. " +
		"Plain words, 3 to 6 sentences, no headings: what stands out (new problems, ones that came back, the busiest), which apps, and what to look at first. " +
		"Only what the list says; no invented causes."
	out, err := cl.Complete(ctx, sys, "The issues seen in the last "+map[bool]string{true: "7 days", false: "24 hours"}[in.Range == "7d"]+":\n"+b.String(), false)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "AI: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"text": strings.TrimSpace(out)})
}

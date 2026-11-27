package server

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"

	core "github.com/abjelosevic88/pocket-homelab-core/sdk"
	"github.com/abjelosevic88/pocket-homelab-errors/internal/store"
)

// Pocket Core app links (docs/links.md in pocket-homelab-core), Errors as
// the target: another Pocket app the person allowed in Core may act here for
// them with a pcx_ token, only within these scopes. Every route not listed
// in linkRoutes stays closed to it (403).

var linkOffers = []core.Offer{
	{Scope: "errors:read", Name: "See your errors", Description: "Read your projects, the issues they have (what broke, where, how often, since when, whether resolved), their latest events with the stack and, for an admin, this box's log lines (search, counts, repeating messages, web traffic) — for your journal (\"what broke today\"), search, dashboards and Pocket AI."},
	{Scope: "errors:write", Name: "Send errors and close issues", Description: "Report an app's own errors here (they go to a project named after that app) and resolve, ignore or reopen the issues of that project. It changes nothing else."},
}

// portable is what Pocket Move may copy out and bring in for a person
// (docs/move.md in pocket-homelab-core); Register adds move:export/import.
// The box's log lines are not a person's and stay out.
var portable = &core.Portable{
	Exports: []core.PortableExport{
		{Path: "/api/v1/export.json", File: "errors.json", Kind: core.KindPocket, Title: "Your projects with their keys, every issue with its counts, kept events and links"},
		{Path: "/api/v1/export.csv", File: "issues.csv", Kind: core.KindCSV, Title: "Your issues as a table"},
	},
	Imports: []core.PortableImport{
		{Path: "/api/v1/import", Kind: core.KindPocket, Form: "file", Title: "Adds the projects, issues and events from another box; what you already have is left alone"},
	},
}

// capabilities is what Pocket Search, Pocket Day and Pocket Hub may read
// here for a person (docs/capabilities.md in pocket-homelab-core); Register
// adds the pocket:* offers. Only issue titles, projects, counts and times:
// no event, stack or log line, and nothing can be resolved or ignored.
var capabilities = core.Capabilities{
	Search: &core.Searchable{What: "Your issues, by what broke, where it broke or the project", Kinds: []string{"issue"}, Bangs: []string{"errors", "error", "issue"}},
	Day:    &core.DayFeed{Label: "Errors", One: "issue", Many: "issues", What: "The issues first seen that day and the ones that came back after you resolved them, with how many times each happened"},
	Widget: &core.Widget{Title: "Errors", What: "The open issues happening today, new ones first, then the busiest"},
}

type linkRoute struct {
	methods string
	path    *regexp.Regexp
	scope   string // "" = any linked app
}

var linkRoutes = []linkRoute{
	{"GET", regexp.MustCompile(`^/api/v1/auth/me$`), ""},
	{"GET", regexp.MustCompile(`^/api/v1/(summary|search|issues|projects|day/\d{4}-\d{2}-\d{2})$`), "errors:read"},
	{"GET", regexp.MustCompile(`^/api/v1/(issues|projects)/\d+$`), "errors:read"},
	{"GET", regexp.MustCompile(`^/api/v1/issues/\d+/(events|events/\d+|latest|hours|logs)$`), "errors:read"},
	// the box's log lines: admins only (checked in the handlers)
	{"GET", regexp.MustCompile(`^/api/v1/logs(/(status|histogram|overview|patterns|web|sources|lines/\d+/context))?$`), "errors:read"},
	{"POST", regexp.MustCompile(`^/api/v1/report$`), "errors:write"},
	{"POST", regexp.MustCompile(`^/api/v1/issues/\d+/status$`), "errors:write"},
	{"POST", regexp.MustCompile(`^/api/v1/issues/status$`), "errors:write"},
}

var errLinkScope = errors.New("outside the link's scope")

// linkAllows reports whether a link token with these scopes may make this request.
func linkAllows(scopes []string, r *http.Request) bool {
	if portable.LinkAllows(scopes, r) || capabilities.LinkAllows(scopes, r) {
		return true
	}
	for _, lr := range linkRoutes {
		if !strings.Contains(" "+lr.methods+" ", " "+r.Method+" ") || !lr.path.MatchString(r.URL.Path) {
			continue
		}
		if lr.scope == "" {
			return true
		}
		for _, sc := range scopes {
			if sc == lr.scope {
				return true
			}
		}
	}
	return false
}

// linkUser validates a pcx_ token, checks the route against its scopes and
// maps it to the person it acts for. The second value is the acting app's slug.
func (s *Server) linkUser(ctx context.Context, tok string, r *http.Request) (*store.User, string, error) {
	in, err := s.core.Introspect(ctx, tok)
	if err != nil {
		return nil, "", err
	}
	if !in.Active || !in.Delegated() || in.Audience != "errors" || in.User == nil || in.User.Email == "" || in.App == nil || in.App.Slug == "" {
		return nil, "", http.ErrNoCookie
	}
	if !linkAllows(strings.Fields(in.Scope), r) {
		return nil, "", errLinkScope
	}
	u, err := s.coreIdentityUser(ctx, in)
	return u, in.App.Slug, err
}

const linkAppKey ctxKey = 2

// linkApp is the acting app's slug when the request came in on a link token.
func linkApp(ctx context.Context) string {
	a, _ := ctx.Value(linkAppKey).(string)
	return a
}

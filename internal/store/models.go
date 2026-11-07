package store

import (
	"encoding/json"
	"strings"
)

// User is an account on this box.
type User struct {
	ID           int64  `json:"id"`
	Email        string `json:"email"`
	Name         string `json:"name"`
	PasswordHash string `json:"-"`
	Role         string `json:"role"` // admin | member
	CreatedAt    string `json:"created_at"`
}

// APIToken is a personal access token (hash stored, plaintext shown once).
type APIToken struct {
	ID         int64   `json:"id"`
	UserID     int64   `json:"user_id"`
	Name       string  `json:"name"`
	Prefix     string  `json:"prefix"`
	LastUsedAt *string `json:"last_used_at"`
	CreatedAt  string  `json:"created_at"`
}

// Conn is one person's connection to a sibling Pocket app.
type Conn struct {
	Mode      string `json:"mode,omitempty"`
	URL       string `json:"url,omitempty"`
	Token     string `json:"token,omitempty"`
	PublicURL string `json:"public_url,omitempty"`
}

// Settings are the box-wide values plus one person's preferences and their
// own connections.
type Settings struct {
	Title         string `json:"title"`
	SetupComplete bool   `json:"setup_complete"`

	DateFormat string `json:"date_format"`
	TimeFormat string `json:"time_format"`
	WeekStart  int    `json:"week_start"` // 1 = Monday, 7 = Sunday

	// Errors.
	KeepDays int  `json:"keep_days"` // events older than this are tidied away (issues stay)
	ScrubIPs bool `json:"scrub_ips"` // drop the app users' IP addresses
	AIHelp   bool `json:"ai_help"`

	// Telling the person: through Pocket Alerts, a push of their own, both
	// or not at all ("auto" = Pocket Alerts when it works, else the push).
	NotifyVia    string `json:"notify_via"` // auto | alerts | push | both | off
	NotifyURL    string `json:"notify_url"`
	NotifyFormat string `json:"notify_format"`

	// This box's containers (admins only): their logs are read for errors.
	WatchDocker  bool   `json:"watch_docker"`
	WatchPattern string `json:"watch_pattern"` // names, * allowed, comma separated

	// Pocket Projects: the team an error becomes an issue in.
	ProjectsTeam string `json:"projects_team"`

	// Each sibling Pocket app, by slug.
	Conns map[string]Conn `json:"conns"`

	// AI: Pocket AI through Core, or an OpenAI-compatible API with the
	// person's own key.
	AIProvider    string `json:"ai_provider"` // auto | pocket | openai
	AIPocketModel string `json:"ai_pocket_model"`
	OpenAIKey     string `json:"openai_api_key"`
	OpenAIModel   string `json:"openai_model"`
	OpenAIBaseURL string `json:"openai_base_url"`
}

func toJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// CleanTags trims, de-duplicates and caps a list of tags.
func CleanTags(in []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, t := range in {
		t = strings.TrimSpace(strings.TrimPrefix(t, "#"))
		k := strings.ToLower(t)
		if t == "" || seen[k] || len(out) >= 20 {
			continue
		}
		seen[k] = true
		out = append(out, t)
	}
	return out
}

// Platforms a project can be (the setup help differs).
var Platforms = []string{"javascript", "go", "python", "node", "docker", "other"}

// Levels from worst to mildest.
var Levels = []string{"fatal", "error", "warning", "info", "debug"}

// LevelRank orders levels: fatal 0 … debug 4 (unknown = error).
func LevelRank(l string) int {
	for i, v := range Levels {
		if v == l {
			return i
		}
	}
	return 1
}

// Project is one app, site or service that sends errors.
type Project struct {
	ID          int64   `json:"id"`
	UserID      int64   `json:"-"`
	Name        string  `json:"name"`
	Platform    string  `json:"platform"`
	PublicKey   string  `json:"public_key"`
	Source      string  `json:"source"`
	Container   string  `json:"container"`
	AppSlug     string  `json:"app_slug"`
	NotifyNew   bool    `json:"notify_new"`
	NotifyAgain bool    `json:"notify_again"`
	NotifySpike int     `json:"notify_spike"`
	MinLevel    string  `json:"min_level"`
	RateLimit   int     `json:"rate_limit"`
	Archived    bool    `json:"archived"`
	LastEventAt *string `json:"last_event_at"`
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`

	// Filled in for lists.
	Open    int   `json:"open"`
	Day     int   `json:"events_24h"`
	Hours   []int `json:"hours,omitempty"` // the last 24 hours, oldest first
	Unknown bool  `json:"-"`
}

// Issue is one problem: the events with the same fingerprint.
type Issue struct {
	ID           int64   `json:"id"`
	UserID       int64   `json:"-"`
	ProjectID    int64   `json:"project_id"`
	Fingerprint  string  `json:"fingerprint"`
	Title        string  `json:"title"`
	Culprit      string  `json:"culprit"`
	Type         string  `json:"type"`
	Level        string  `json:"level"`
	Status       string  `json:"status"`
	Regressed    bool    `json:"regressed"`
	Times        int     `json:"times"`
	FirstSeen    string  `json:"first_seen"`
	LastSeen     string  `json:"last_seen"`
	FirstRelease string  `json:"first_release"`
	LastRelease  string  `json:"last_release"`
	Environment  string  `json:"environment"`
	ResolvedAt   *string `json:"resolved_at"`
	IgnoredUntil *string `json:"ignored_until"`
	Note         string  `json:"note"`
	AIText       string  `json:"ai_text"`
	AIAt         *string `json:"ai_at"`
	SpikeAt      *string `json:"-"`
	UpdatedAt    string  `json:"updated_at"`

	// Filled in for lists and pages.
	ProjectName string `json:"project_name"`
	AppSlug     string `json:"app_slug"`
	People      int    `json:"people"`
	Day         int    `json:"events_24h"`
	Hours       []int  `json:"hours,omitempty"`
	New         bool   `json:"new"` // first seen in the last 24 hours
}

// Event is one time an issue happened.
type Event struct {
	ID          int64     `json:"id"`
	UserID      int64     `json:"-"`
	ProjectID   int64     `json:"project_id"`
	IssueID     int64     `json:"issue_id"`
	EventID     string    `json:"event_id"`
	At          string    `json:"at"`
	ReceivedAt  string    `json:"received_at"`
	Level       string    `json:"level"`
	Message     string    `json:"message"`
	Environment string    `json:"environment"`
	Release     string    `json:"release"`
	Platform    string    `json:"platform"`
	Who         string    `json:"who"`
	Data        EventData `json:"data"`
}

// EventData is everything an event brought, in one shape whatever sent it.
type EventData struct {
	Exceptions  []Exception          `json:"exceptions,omitempty"` // the one raised last; causes before it
	Message     string               `json:"message,omitempty"`
	Logger      string               `json:"logger,omitempty"`
	Transaction string               `json:"transaction,omitempty"`
	Breadcrumbs []Breadcrumb         `json:"breadcrumbs,omitempty"`
	Request     *Request             `json:"request,omitempty"`
	User        map[string]string    `json:"user,omitempty"`
	Tags        [][2]string          `json:"tags,omitempty"`
	Contexts    map[string]KeyValues `json:"contexts,omitempty"`
	Extra       map[string]any       `json:"extra,omitempty"`
	ServerName  string               `json:"server_name,omitempty"`
	SDK         string               `json:"sdk,omitempty"`
	LogLines    []string             `json:"log_lines,omitempty"` // a container's lines around it
	Fingerprint []string             `json:"fingerprint,omitempty"`
}

// KeyValues is a context (browser, os, runtime, device) as ordered pairs.
type KeyValues [][2]string

// Exception is one error with its stack.
type Exception struct {
	Type      string  `json:"type"`
	Value     string  `json:"value"`
	Module    string  `json:"module,omitempty"`
	Mechanism string  `json:"mechanism,omitempty"`
	Handled   *bool   `json:"handled,omitempty"`
	Frames    []Frame `json:"frames,omitempty"` // outermost first, the one that failed last
}

// Frame is one line of a stack.
type Frame struct {
	Function    string   `json:"function,omitempty"`
	Module      string   `json:"module,omitempty"`
	File        string   `json:"file,omitempty"`
	Line        int      `json:"line,omitempty"`
	Col         int      `json:"col,omitempty"`
	InApp       bool     `json:"in_app"`
	PreContext  []string `json:"pre_context,omitempty"`
	ContextLine string   `json:"context_line,omitempty"`
	PostContext []string `json:"post_context,omitempty"`
}

// Breadcrumb is something that happened before the error.
type Breadcrumb struct {
	At       string            `json:"at,omitempty"`
	Type     string            `json:"type,omitempty"`
	Category string            `json:"category,omitempty"`
	Level    string            `json:"level,omitempty"`
	Message  string            `json:"message,omitempty"`
	Data     map[string]string `json:"data,omitempty"`
}

// Request is the web request the error happened in.
type Request struct {
	Method  string      `json:"method,omitempty"`
	URL     string      `json:"url,omitempty"`
	Query   string      `json:"query,omitempty"`
	Headers [][2]string `json:"headers,omitempty"`
	Data    string      `json:"data,omitempty"`
}

// IssueLink is what an issue became in another app.
type IssueLink struct {
	ID        int64  `json:"id"`
	IssueID   int64  `json:"issue_id"`
	App       string `json:"app"`
	Ref       string `json:"ref"`
	Title     string `json:"title"`
	URL       string `json:"url"`
	CreatedAt string `json:"created_at"`
}

// RequestURL is the address of the request the event happened in, "" without one.
func (d EventData) RequestURL() string {
	if d.Request == nil {
		return ""
	}
	return d.Request.URL
}

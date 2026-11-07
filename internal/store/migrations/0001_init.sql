CREATE TABLE users (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  email         TEXT NOT NULL UNIQUE,
  name          TEXT NOT NULL DEFAULT '',
  password_hash TEXT NOT NULL DEFAULT '',
  role          TEXT NOT NULL DEFAULT 'member',
  created_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  updated_at    TEXT
);

CREATE TABLE sessions (
  id         TEXT PRIMARY KEY,
  user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  expires_at TEXT NOT NULL,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

CREATE TABLE api_tokens (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name         TEXT NOT NULL DEFAULT '',
  token_hash   TEXT NOT NULL UNIQUE,
  prefix       TEXT NOT NULL DEFAULT '',
  last_used_at TEXT,
  created_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

-- Box-wide settings (title, setup flag); everything else is personal.
CREATE TABLE settings (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL DEFAULT ''
);

CREATE TABLE user_settings (
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  key     TEXT NOT NULL,
  value   TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (user_id, key)
);

CREATE TABLE cache (
  key        TEXT PRIMARY KEY,
  value      TEXT NOT NULL DEFAULT '',
  fetched_at TEXT NOT NULL
);



-- ---------- Pocket Errors: everything below is per person ----------
-- A project is one app, site or service that sends errors here (with a
-- Sentry-style key, through a Pocket Core link, or from a watched container's
-- logs). Errors that are the same problem are grouped into one issue; each
-- time it happens is an event. Times are UTC ISO-8601.

CREATE TABLE projects (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id       INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name          TEXT NOT NULL DEFAULT '',
  platform      TEXT NOT NULL DEFAULT 'other',   -- javascript | go | python | node | docker | other
  public_key    TEXT NOT NULL UNIQUE,            -- the key in the DSN
  source        TEXT NOT NULL DEFAULT '',        -- '' = you, 'docker' = a watched container, else the Pocket app that reports
  container     TEXT NOT NULL DEFAULT '',        -- the container watched (source docker)
  app_slug      TEXT NOT NULL DEFAULT '',        -- the Pocket app it is, when known (icon, link)
  notify_new    INTEGER NOT NULL DEFAULT 1,
  notify_again  INTEGER NOT NULL DEFAULT 1,      -- a resolved issue came back
  notify_spike  INTEGER NOT NULL DEFAULT 0,      -- events per hour at or above; 0 = never
  min_level     TEXT NOT NULL DEFAULT 'error',   -- what a container's logs count from: error | warning
  rate_limit    INTEGER NOT NULL DEFAULT 0,      -- events a minute kept; 0 = the default
  archived      INTEGER NOT NULL DEFAULT 0,
  last_event_at TEXT,
  created_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  updated_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE INDEX projects_user ON projects(user_id, archived, name);
CREATE UNIQUE INDEX projects_container ON projects(user_id, container) WHERE container <> '';
CREATE UNIQUE INDEX projects_source ON projects(user_id, source) WHERE source <> '' AND source <> 'docker';

-- One problem: every event with the same fingerprint.
CREATE TABLE issues (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id       INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  project_id    INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  fingerprint   TEXT NOT NULL,
  title         TEXT NOT NULL DEFAULT '',
  culprit       TEXT NOT NULL DEFAULT '',
  type          TEXT NOT NULL DEFAULT '',        -- the exception type, '' for a message
  level         TEXT NOT NULL DEFAULT 'error',   -- fatal | error | warning | info | debug
  status        TEXT NOT NULL DEFAULT 'open',    -- open | resolved | ignored
  regressed     INTEGER NOT NULL DEFAULT 0,      -- came back after it was resolved
  times         INTEGER NOT NULL DEFAULT 0,      -- events ever
  first_seen    TEXT NOT NULL,
  last_seen     TEXT NOT NULL,
  first_release TEXT NOT NULL DEFAULT '',
  last_release  TEXT NOT NULL DEFAULT '',
  environment   TEXT NOT NULL DEFAULT '',
  resolved_at   TEXT,
  ignored_until TEXT,                            -- NULL with status ignored = for good
  note          TEXT NOT NULL DEFAULT '',
  ai_text       TEXT NOT NULL DEFAULT '',
  ai_at         TEXT,
  spike_at      TEXT,                            -- last time a spike was told
  updated_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  UNIQUE (project_id, fingerprint)
);
CREATE INDEX issues_user ON issues(user_id, status, last_seen DESC);
CREATE INDEX issues_project ON issues(project_id, status, last_seen DESC);

-- Each time it happened (the newest ones; older ones are tidied away after
-- the days the person keeps). data is the event as JSON (stack, breadcrumbs,
-- request, tags, contexts).
CREATE TABLE events (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  project_id  INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  issue_id    INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
  event_id    TEXT NOT NULL DEFAULT '',
  at          TEXT NOT NULL,                     -- when it happened
  received_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  level       TEXT NOT NULL DEFAULT 'error',
  message     TEXT NOT NULL DEFAULT '',
  environment TEXT NOT NULL DEFAULT '',
  release     TEXT NOT NULL DEFAULT '',
  platform    TEXT NOT NULL DEFAULT '',
  who         TEXT NOT NULL DEFAULT '',          -- the app's user it happened to (id, email or address)
  data        TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX events_issue ON events(issue_id, at DESC);
CREATE INDEX events_user ON events(user_id, received_at);
CREATE INDEX events_project ON events(project_id, event_id);

-- How many times an issue happened each hour (kept longer than events, for the charts).
CREATE TABLE issue_hours (
  issue_id INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
  user_id  INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  hour     TEXT NOT NULL,                        -- YYYY-MM-DDTHH (UTC)
  n        INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (issue_id, hour)
) WITHOUT ROWID;
CREATE INDEX issue_hours_user ON issue_hours(user_id, hour);

-- Which of the app's users an issue reached (for "how many people").
CREATE TABLE issue_people (
  issue_id INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
  who      TEXT NOT NULL,
  PRIMARY KEY (issue_id, who)
) WITHOUT ROWID;

-- What an issue became in the other apps (a Pocket Projects issue, a task).
CREATE TABLE issue_links (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  issue_id   INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
  app        TEXT NOT NULL,
  ref        TEXT NOT NULL DEFAULT '',
  title      TEXT NOT NULL DEFAULT '',
  url        TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE INDEX issue_links_issue ON issue_links(user_id, issue_id);

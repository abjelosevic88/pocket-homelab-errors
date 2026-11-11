# API

Everything the web app does goes through `/api/v1`, with a session cookie or `Authorization: Bearer per_…` (Settings → API & shortcuts). Times are UTC ISO-8601. Every row belongs to the person signed in; another person's ids answer 404.

## Sending errors
| | |
|---|---|
| `POST /api/{project}/store/` · `POST /api/{project}/envelope/` | Sentry's protocol (any Sentry SDK with the project's DSN `https://KEY@host/ID`; key in `X-Sentry-Auth`, `?sentry_key=` or the envelope's `dsn`; gzip and deflate). Transactions, sessions and client reports are skipped. CORS open for browsers |
| `GET /errors.js` | A small browser script: `<script src="…/errors.js" data-dsn="…" data-release="1.0.0">`; `PocketErrors.capture(err)`, `PocketErrors.message(text, level)` |
| `POST /api/v1/projects/{id}/events` | With a token: Sentry's event shape, or simply `{message, level}` or `{type, value, stack}` (a stack as text: JavaScript, Python, Go, Java), plus `environment, release, user, tags, extra, fingerprint` |
| `POST /api/v1/report` | A Pocket app through its Core link (`errors:write`): the same body, into a project named after the app. With a person's own token: `?project_id=`, or `?source=my-phone&name=My%20phone&platform=cocoa` for the sender's own project, made on its first error |

Each project takes 600 errors a minute (its own number in its settings), then answers 429. Passwords, tokens, cookies and keys are filtered out before anything is kept; the app users' addresses too, unless switched off.

## Issues
| | |
|---|---|
| `GET /summary` | `{overview: {open, new, regressed, events_24h, hours: {fatal, error, warning, info}}, latest, watch}` |
| `GET /issues` | `?status=open\|resolved\|ignored\|all&project=&level=&q=&since=24h&sort=last\|first\|times\|people&limit=&offset=` → `{issues, total}` |
| `GET /issues/{id}` | The issue with its project, links and tag counts |
| `GET /issues/{id}/latest` · `GET /issues/{id}/events/{eid}` | An event with `pos, total, newer, older` |
| `GET /issues/{id}/events` | Kept events, newest first (`?before=&limit=`) |
| `GET /issues/{id}/hours?range=24h\|30d` | Counts by hour or by day |
| `GET /issues/{id}/markdown` | The issue and its newest event as Markdown |
| `POST /issues/{id}/status` · `POST /issues/status` | `{status: resolved\|ignored\|open, for: 1h\|24h\|7d\|30d\|""}`, the second with `ids` |
| `PUT /issues/{id}` · `DELETE /issues/{id}` | `{note}` |
| `POST /issues/{id}/explain` | Pocket AI (or your OpenAI key): `{text}`, kept on the issue (`{again: true}` asks again) |
| `POST /issues/{id}/send` | `{to: projects\|tasks, title?, team?, due?}` → a Pocket Projects issue or a Pocket Tasks task with a link back |
| `POST /ai/digest` | `{range: 24h\|7d}` → `{text}` |
| `GET /search?q=` · `GET /day/{date}` | For Pocket Search and Pocket Day |

## Projects
`GET /projects` · `POST /projects` `{name, platform}` · `GET/PUT/DELETE /projects/{id}` (`{name, platform, notify_new, notify_again, notify_spike, min_level, rate_limit, archived, new_key}`) · `POST /projects/{id}/test` · `GET /watch` (the containers read).

## Logs (admins)

Every route takes `range` (`30m`, `24h`, `7d`; also `since`) or `from`/`to` (unix ms or RFC 3339). Lines are newest first; times are unix ms.

| | |
|---|---|
| `GET /api/v1/logs` | lines: `q` (all words; `"a phrase"`, `-not`), `source` (ids or names, commas), `group` (`pocket`, `docker`, `system`, `user`, `kernel`), `level` (that and worse), `pattern`, `before` / `after` (a line id), `limit` (≤ 1000) → `{lines, more}` |
| `GET /api/v1/logs/histogram` | the same filter counted per bar and level |
| `GET /api/v1/logs/lines/{id}/context?n=25` | its source's lines around one |
| `GET /api/v1/logs/overview` | everything written (kept or only counted): per bar and level, totals and the span before, per source, per group, a heat map |
| `GET /api/v1/logs/patterns` | messages that repeat: `sort` = `top`, `errors` or `new`, `source`, `limit` |
| `GET /api/v1/logs/web` | Caddy's requests: per bar and status class, answer times, per site |
| `GET /api/v1/logs/sources`, `PUT /api/v1/logs/sources/{id}` | sources and their counts; `{"keep": "all" \| "warn" \| "error" \| "count"}` |
| `GET /api/v1/logs/status`, `PUT /api/v1/logs/settings`, `POST /api/v1/logs/clear` | `{on, pattern, journal, keep_days, max_mb}` |
| `GET /api/v1/logs/export.txt` | the matching lines as text (≤ 50,000) |
| `POST /api/v1/logs/push` | lines from outside the box (a phone, a laptop, an app), with an admin's token: JSON `{"source": "my-phone", "name": "My phone", "lines": [{"at", "level", "message", "fields"}]}` (≤ 1000 lines, 1 MB), or text / NDJSON with `?source=&name=` (iPhone and BSD syslog lines keep their time and process). Group `device`; a line sent twice is kept once; a clock more than 10 min ahead is now, lines older than the logs keep are dropped |
| `GET /pocket-logs.sh` | a script that sends what it reads on stdin: `idevicesyslog \| pocket-logs.sh iphone "iPhone"` on a Mac paired with the phone |
| `GET /api/v1/issues/{id}/logs?event=` | what the issue's container wrote two minutes either side of an event |

## Everything else
`GET /export.json` · `POST /import` (twice is the same as once) · `GET /export.csv` · `GET /backup` (admins).

## Other Pocket apps (Pocket Core links)
`errors:read` — `GET summary, search, issues, issues/{id}, issues/{id}/(events|latest|hours), projects, projects/{id}, day/{date}`; `errors:write` — `POST report`, `POST issues/{id}/status`, `POST issues/status` (only the issues of that app's own project). Events: `errors.issue_new`, `errors.issue_regressed`, `errors.issue_spike`, `errors.issue_resolved`.

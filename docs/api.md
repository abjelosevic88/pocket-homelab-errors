# API

Everything the web app does goes through `/api/v1`, with a session cookie or `Authorization: Bearer per_…` (Settings → API & shortcuts). Times are UTC ISO-8601. Every row belongs to the person signed in; another person's ids answer 404.

## Sending errors
| | |
|---|---|
| `POST /api/{project}/store/` · `POST /api/{project}/envelope/` | Sentry's protocol (any Sentry SDK with the project's DSN `https://KEY@host/ID`; key in `X-Sentry-Auth`, `?sentry_key=` or the envelope's `dsn`; gzip and deflate). Transactions, sessions and client reports are skipped. CORS open for browsers |
| `GET /errors.js` | A small browser script: `<script src="…/errors.js" data-dsn="…" data-release="1.0.0">`; `PocketErrors.capture(err)`, `PocketErrors.message(text, level)` |
| `POST /api/v1/projects/{id}/events` | With a token: Sentry's event shape, or simply `{message, level}` or `{type, value, stack}` (a stack as text: JavaScript, Python, Go, Java), plus `environment, release, user, tags, extra, fingerprint` |
| `POST /api/v1/report` | A Pocket app through its Core link (`errors:write`): the same body, into a project named after the app |

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

## Everything else
`GET /export.json` · `POST /import` (twice is the same as once) · `GET /export.csv` · `GET /backup` (admins).

## Other Pocket apps (Pocket Core links)
`errors:read` — `GET summary, search, issues, issues/{id}, issues/{id}/(events|latest|hours), projects, projects/{id}, day/{date}`; `errors:write` — `POST report`, `POST issues/{id}/status`, `POST issues/status` (only the issues of that app's own project). Events: `errors.issue_new`, `errors.issue_regressed`, `errors.issue_spike`, `errors.issue_resolved`.

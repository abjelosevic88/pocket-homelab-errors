# Configuration

Environment variables; every one has a default. Personal settings (how long events are kept, how you are told, connections, AI) live in the UI per person — the `OPENAI_*` and `NOTIFY_*` values below are copied once into the first admin's settings.

| Variable | Default | What it does |
|---|---|---|
| `BASE_URL` | – | Where the app is (the DSNs it shows, links in pushes and other apps, the OIDC callback) |
| `HOST` / `PORT` | `0.0.0.0` / `8080` | Where it listens |
| `DATA_DIR` | `./data` | The database and the Pocket Core token |
| `DB_PATH` | `$DATA_DIR/pocket-errors.db` | The SQLite file |
| `AUTH_MODE` | `local` | `local` (passwords), `proxy` (a header from your reverse proxy), `none` (one user, for trying it out) |
| `SECURE_COOKIES` | `false` | Set `true` behind HTTPS |
| `ADMIN_EMAIL` / `ADMIN_PASSWORD` / `ADMIN_NAME` | – | The first account, made on an empty database |
| `OIDC_ISSUER` / `OIDC_CLIENT_ID` / `OIDC_CLIENT_SECRET` | – | Sign in with an OpenID Connect provider (not needed with Pocket Core) |
| `OPENAI_API_KEY` / `OPENAI_MODEL` / `OPENAI_BASE_URL` | – | AI without Pocket Core |
| `NOTIFY_URL` / `NOTIFY_FORMAT` | – | The owner's push (ntfy, Apprise or JSON) |
| `DOCKER_SOCKET` | `/var/run/docker.sock` | Where the Docker Engine answers; mount it to have this box's containers' logs read (Settings → This box, admins). Only container lists and logs are read |
| `INGEST_URL` | `CORE_APP_URL` when it differs from `BASE_URL` | The address other containers on this box send errors to; shown as a second DSN |
| `CORE_URL` / `CORE_REGISTRATION_KEY` / `CORE_APP_URL` | – | Pocket Core |
| `TRUST_PROXY` | `true` | Take the client address from `X-Forwarded-For` |
| `METRICS_ENABLED` | `true` | Prometheus `/metrics` |
| `MAX_BODY_BYTES` | `67108864` | Largest JSON body (an export being imported) |
| `DEMO_DATA` | `false` | A month of made-up errors from five projects — nothing called online |
| `LOG_LEVEL` / `LOG_FORMAT` | `info` / `text` | |

Connections to Pocket Alerts, Projects and Tasks are made per person under Settings → Connected apps: through Pocket Core (nothing to paste) or directly with the app's URL and an API token.

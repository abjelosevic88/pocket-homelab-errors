# Backup

Everything lives in `DATA_DIR`: the SQLite file `pocket-errors.db` (WAL mode) and `core.token*` (the Pocket Core registration). Take a consistent copy of the database with `pocket-errors backup` (writes `DATA_DIR/backups/pocket-errors-<time>.db` with `VACUUM INTO`) or Settings → Backup & data → Download database (admin). Back up that copy, not the live file.

Restore: stop the app, replace `pocket-errors.db` (remove `-wal`/`-shm` files), start.

Every person can also export their own projects (with their keys), issues, kept events and links as JSON (`/api/v1/export.json`, back in with Settings → Backup & data, twice is the same as once), and their issues as CSV.

#!/usr/bin/env bash
# pocket-logs.sh — send lines to Pocket Errors → Logs from any computer.
#
# Whatever comes in on stdin goes to your box's Logs as one source, a batch
# every two seconds (or every 500 lines). Lines that say when they were
# written (an iPhone's or a Mac's syslog) keep that time.
#
#   export POCKET_ERRORS_URL=https://errors.example.com
#   export POCKET_ERRORS_TOKEN=per_…          # Settings → API tokens (an admin's)
#
#   # an iPhone's system log, through a Mac it is paired with (brew install libimobiledevice):
#   idevicesyslog | pocket-logs.sh iphone "iPhone"
#   idevicesyslog -n | pocket-logs.sh iphone "iPhone"      # over Wi-Fi
#
#   # anything else
#   tail -F /var/log/system.log | pocket-logs.sh macbook "MacBook"
#   some-command 2>&1 | pocket-logs.sh my-job "Nightly job"
#
# Needs bash and curl. Nothing is kept on this computer.
set -u
URL="${POCKET_ERRORS_URL:-}"
TOKEN="${POCKET_ERRORS_TOKEN:-}"
if [ -z "$URL" ] || [ -z "$TOKEN" ]; then
  echo "pocket-logs.sh: set POCKET_ERRORS_URL and POCKET_ERRORS_TOKEN (Pocket Errors → Settings → API tokens)" >&2
  exit 2
fi
SRC=$(printf '%s' "${1:-$(hostname -s 2>/dev/null || echo device)}" | tr 'A-Z' 'a-z' | tr -c 'a-z0-9-' '-' | sed 's/--*/-/g; s/^-*//; s/-*$//' | cut -c1-48)
if [ "${#SRC}" -lt 2 ] || [ "$SRC" = docker ]; then
  echo "pocket-logs.sh: name the source with 2 to 48 letters, digits and dashes, e.g. iphone" >&2
  exit 2
fi
LABEL="${2:-${1:-$SRC}}"
EVERY="${POCKET_LOGS_EVERY:-2}"
MAX="${POCKET_LOGS_BATCH:-500}"
buf=$(mktemp "${TMPDIR:-/tmp}/pocket-logs.XXXXXX")
trap 'send; rm -f "$buf"' EXIT
trap 'exit 0' INT TERM
n=0

send() {
  [ -s "$buf" ] || return 0
  for try in 1 2 3; do
    if curl -fsS -m 30 -o /dev/null -X POST \
      -H "Authorization: Bearer $TOKEN" -H "Content-Type: text/plain; charset=utf-8" -H "X-Source-Label: $LABEL" \
      --data-binary @"$buf" "${URL%/}/api/v1/logs/push?source=$SRC"; then
      : > "$buf"; n=0; return 0
    fi
    sleep $((try * 2))
  done
  echo "pocket-logs.sh: could not send $n lines; dropped them" >&2
  : > "$buf"; n=0
}

while true; do
  if IFS= read -r -t "$EVERY" line; then
    printf '%s\n' "$line" >> "$buf"
    n=$((n + 1))
    [ "$n" -ge "$MAX" ] && send
  else
    rc=$?
    [ -n "${line:-}" ] && { printf '%s\n' "$line" >> "$buf"; n=$((n + 1)); }
    send
    [ "$rc" -le 128 ] && break   # the end of the input, not a timeout
  fi
done

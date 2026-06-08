#!/usr/bin/env bash
# Wait until every docker-compose service reports healthy. Services
# without a healthcheck count as healthy immediately.
set -euo pipefail

TIMEOUT="${TIMEOUT:-120}"
INTERVAL=2
elapsed=0

if ! command -v docker >/dev/null 2>&1; then
  echo "docker not installed or not in PATH" >&2
  exit 1
fi

while [ "$elapsed" -lt "$TIMEOUT" ]; do
  # `docker compose ps --format json` emits one JSON object per line. We
  # use jq to filter for unhealthy/starting states. If jq is missing,
  # fall back to a grep-based check.
  if command -v jq >/dev/null 2>&1; then
    unhealthy=$(docker compose ps --format json 2>/dev/null \
      | jq -r 'select(.Health == "unhealthy" or .Health == "starting") | .Name' \
      | tr '\n' ' ')
  else
    unhealthy=$(docker compose ps --format '{{.Name}} {{.Health}}' 2>/dev/null \
      | awk '$2=="starting" || $2=="unhealthy" {printf "%s ", $1}')
  fi
  if [ -z "${unhealthy// }" ]; then
    echo "All services healthy."
    exit 0
  fi
  echo "Waiting for:${unhealthy:+ }${unhealthy}"
  sleep "$INTERVAL"
  elapsed=$((elapsed + INTERVAL))
done

echo "Timeout waiting for services. Current state:" >&2
docker compose ps >&2
exit 1

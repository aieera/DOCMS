#!/usr/bin/env bash
# dev-down.sh — stop the backend Go services + infra containers.
# Frontend stays running (Ctrl+C in its terminal).
#
# Usage:
#   bash scripts/dev-down.sh           # stop services + infra (keep volumes)
#   bash scripts/dev-down.sh --purge   # also drop volumes (DESTROYS data)

set -euo pipefail
cd "$(dirname "$0")/.."

# ---- 1. Kill Go services ----------------------------------------------------
if [[ -f .run/services.pids ]]; then
  echo "==> stopping Go services"
  while IFS=':' read -r pid svc; do
    if [[ -n "$pid" ]]; then
      if [[ -x /c/Windows/System32/taskkill.exe ]]; then
        /c/Windows/System32/taskkill.exe //F //PID "$pid" >/dev/null 2>&1 || true
      else
        kill -9 "$pid" >/dev/null 2>&1 || true
      fi
    fi
  done < .run/services.pids
  rm -f .run/services.pids
fi

# ---- 2. Stop containers -----------------------------------------------------
echo "==> stopping infra containers"
if [[ "${1:-}" == "--purge" ]]; then
  docker compose down -v
else
  docker compose down
fi

echo "done."

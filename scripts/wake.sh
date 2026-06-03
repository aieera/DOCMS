#!/usr/bin/env bash
#
# scripts/wake.sh — bring the SeDoc stack back online after a
# Docker/WSL restart.
#
# Handles the three recurring failure modes:
#   1. WSL+Docker port-shuffle — container is "healthy" but its host
#      port is unreachable. Detected by probing each host port; fixed
#      by `docker restart <container>`.
#   2. Cold-start crash-loop — services like search/intelligence FATAL
#      on first dial to Postgres/OpenSearch and rely on Docker's
#      restart policy to recover. We give them up to 90s before
#      declaring a port truly dead.
#   3. Stale images — services running last week's binary won't have
#      today's routes (silent 404s). Opt-in `--rebuild` refreshes all
#      images before bringing the stack up.
#
# Usage:
#   bash scripts/wake.sh                  # default: up + verify + restart dead ports
#   bash scripts/wake.sh --rebuild        # also rebuild all images (~5-10 min)
#   bash scripts/wake.sh --rebuild auth   # rebuild only the named services
#   bash scripts/wake.sh --no-up          # skip `compose up`, just probe + restart
#
# Idempotent. Safe to re-run.

set -euo pipefail

# Where the script runs from — usually the repo root via `bash scripts/wake.sh`.
# Resolve relative to the script so `cd somewhere && bash scripts/wake.sh` works.
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

DC="docker compose"

# Service → host API port. Probed for reachability after bringup.
# Order is informational only; checks are sequential but each is fast.
declare -A SERVICES=(
  [auth]=8180
  [policy]=8181
  [document]=8182
  [storage]=8183
  [search]=8184
  [audit]=8185
  [workflow]=8186
  [notification]=8187
  [signature]=8188
  [billing]=8189
  [connector]=8190
  [graphql-gateway]=8191
  [mcp-server]=8192
  [collaboration]=8083
  [intelligence]=8194
)

# ---- args ----
REBUILD=0
SKIP_UP=0
REBUILD_TARGETS=()
while [[ $# -gt 0 ]]; do
  case "$1" in
    --rebuild|-b)
      REBUILD=1
      shift
      # collect any service names following --rebuild as targets
      while [[ $# -gt 0 && "$1" != --* ]]; do
        REBUILD_TARGETS+=("$1")
        shift
      done
      ;;
    --no-up)
      SKIP_UP=1
      shift
      ;;
    --help|-h)
      sed -n '3,30p' "$0" | sed 's/^# \{0,1\}//'
      exit 0
      ;;
    *)
      printf '\033[31munknown arg: %s\033[0m\n' "$1" >&2
      exit 2
      ;;
  esac
done

# ---- pretty output ----
cyan()   { printf '\033[36m%s\033[0m\n' "$*"; }
green()  { printf '\033[32m%s\033[0m\n' "$*"; }
yellow() { printf '\033[33m%s\033[0m\n' "$*"; }
red()    { printf '\033[31m%s\033[0m\n' "$*"; }

# Probe a host port — any HTTP response (even 404 / 401) means the
# port is reachable. Only 000 (connection refused / timeout) is
# treated as "dead". A bare slash is used so we don't have to know
# each service's route table.
probe_port() {
  local code
  code=$(curl -s -o /dev/null -w '%{http_code}' --max-time 2 "http://localhost:$1/" 2>/dev/null || echo "000")
  [ -n "$code" ] && [ "$code" != "000" ]
}

# Report image ages — warn if anything is >7 days old, since stale
# images cause silent 404s on recently-added routes.
warn_stale_images() {
  local stale=()
  for svc in "${!SERVICES[@]}"; do
    local img="vaultdms-dev-${svc}:latest"
    if ! docker image inspect "$img" >/dev/null 2>&1; then continue; fi
    local created
    created=$(docker image inspect "$img" --format '{{.Created}}' 2>/dev/null)
    local age_days
    age_days=$(( ( $(date +%s) - $(date -d "$created" +%s 2>/dev/null || echo "$(date +%s)") ) / 86400 ))
    if [ "$age_days" -gt 7 ]; then
      stale+=("$svc ($age_days days)")
    fi
  done
  if [ ${#stale[@]} -gt 0 ]; then
    yellow "==> image age warning — these are older than 7 days:"
    for s in "${stale[@]}"; do echo "      $s"; done
    yellow "    If a route returns 404 with body '404 page not found',"
    yellow "    it's likely missing from the stale binary. Rerun with --rebuild."
    echo
  fi
}

# ---- 1. optional rebuild ----
if [ "$REBUILD" = 1 ]; then
  if [ ${#REBUILD_TARGETS[@]} -gt 0 ]; then
    cyan "==> docker compose build ${REBUILD_TARGETS[*]}"
    $DC build "${REBUILD_TARGETS[@]}"
  else
    cyan "==> docker compose build (all services — ~5-10 min)"
    $DC build
  fi
fi

# ---- 2. bring stack up ----
if [ "$SKIP_UP" = 0 ]; then
  cyan "==> docker compose up -d"
  $DC up -d --remove-orphans
fi

# ---- 3. wait for boot ----
cyan "==> waiting for services to settle (max 90s)"
TOTAL=${#SERVICES[@]}
last_ok=-1
for i in {1..18}; do
  sleep 5
  ok=0
  for svc in "${!SERVICES[@]}"; do
    if probe_port "${SERVICES[$svc]}"; then ok=$((ok+1)); fi
  done
  if [ "$ok" != "$last_ok" ]; then
    printf '  [%2ds] %d/%d responding\n' "$((i*5))" "$ok" "$TOTAL"
    last_ok=$ok
  fi
  if [ "$ok" = "$TOTAL" ]; then break; fi
done

# ---- 4. find dead-port containers + restart them ----
dead=()
for svc in "${!SERVICES[@]}"; do
  if ! probe_port "${SERVICES[$svc]}"; then
    dead+=("$svc")
  fi
done

if [ ${#dead[@]} -gt 0 ]; then
  yellow "==> ${#dead[@]} service(s) still unreachable on their host ports:"
  for svc in "${dead[@]}"; do
    echo "      vaultdms-$svc (:${SERVICES[$svc]})"
  done
  cyan "==> restarting those containers"
  for svc in "${dead[@]}"; do
    docker restart "vaultdms-$svc" >/dev/null 2>&1 || true
  done

  sleep 8
  still_dead=()
  for svc in "${dead[@]}"; do
    if ! probe_port "${SERVICES[$svc]}"; then
      still_dead+=("$svc")
    fi
  done

  if [ ${#still_dead[@]} -gt 0 ]; then
    red "==> ${#still_dead[@]} container(s) STILL unreachable after restart:"
    for svc in "${still_dead[@]}"; do
      echo "      vaultdms-$svc — try: $DC up -d --force-recreate $svc"
    done
    echo
    yellow "If a port stays 000 after --force-recreate, the WSL+Docker network proxy is wedged."
    yellow "Recovery sequence:"
    yellow "  1. Save your work."
    yellow "  2. wsl --shutdown                  (from a Windows shell)"
    yellow "  3. Restart Docker Desktop."
    yellow "  4. Re-run this script."
    warn_stale_images
    exit 1
  fi
fi

# ---- 5. final summary ----
green "==> all $TOTAL services responding on their host ports"
echo
for svc in $(echo "${!SERVICES[@]}" | tr ' ' '\n' | sort); do
  printf '  ok  vaultdms-%-18s http://localhost:%s\n' "$svc" "${SERVICES[$svc]}"
done

echo
warn_stale_images

green "ready. open http://localhost:3000"

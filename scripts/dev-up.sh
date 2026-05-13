#!/usr/bin/env bash
# dev-up.sh — bring the whole backend up in one shot.
#
#   1. Stop any stale Go services from a previous run.
#   2. Start infra containers (postgres, redis, nats, opensearch,
#      minio, temporal, clamav, qdrant).
#   3. Wait for postgres to be healthy so the Go services don't
#      crash on first boot.
#   4. Start the 12 Go services via run-all-services.sh.
#   5. Health-probe each service's /healthz; print a summary.
#
# Usage:
#   bash scripts/dev-up.sh           # standard
#   bash scripts/dev-up.sh --fresh   # `docker compose down -v` first (DESTROYS data)
#
# Frontend stays separate — `cd web && npm run dev`.

set -euo pipefail
cd "$(dirname "$0")/.."

INFRA_SERVICES=(postgres redis nats opensearch minio temporal clamav qdrant)
HEALTH_PORTS=(8081 8082 8083 8084 8085 8086 8087 8088 8089 8090 8091 8093)
SERVICE_NAMES=(auth policy document storage search audit workflow notification signature billing connector graphql-gateway)

if [[ "${1:-}" == "--fresh" ]]; then
  echo "==> --fresh: destroying volumes"
  docker compose down -v 2>&1 | tail -5
fi

# ---- 1. Stop stale Go services ---------------------------------------------
echo "==> stopping stale Go services (if any)"
if [[ -f .run/services.pids ]]; then
  while IFS=':' read -r pid svc; do
    if [[ -n "$pid" ]]; then
      # Use Windows taskkill via Git Bash; fall back to plain kill on Linux.
      if [[ -x /c/Windows/System32/taskkill.exe ]]; then
        /c/Windows/System32/taskkill.exe //F //PID "$pid" >/dev/null 2>&1 || true
      else
        kill -9 "$pid" >/dev/null 2>&1 || true
      fi
    fi
  done < .run/services.pids
  rm -f .run/services.pids
fi

# ---- 2. Start infra containers ---------------------------------------------
echo "==> starting infra containers"
if ! docker info >/dev/null 2>&1; then
  echo "ERROR: Docker daemon is not running. Start Docker Desktop and retry."
  exit 1
fi
docker compose up -d "${INFRA_SERVICES[@]}"

# ---- 3. Wait for postgres healthy ------------------------------------------
echo "==> waiting for postgres healthy"
deadline=$(( $(date +%s) + 60 ))
while :; do
  status=$(docker inspect --format '{{.State.Health.Status}}' vaultdms-postgres 2>/dev/null || echo "starting")
  if [[ "$status" == "healthy" ]]; then break; fi
  if [[ $(date +%s) -gt $deadline ]]; then
    echo "ERROR: postgres did not become healthy within 60s"
    exit 1
  fi
  sleep 2
done

# ---- 4. Start Go services in background ------------------------------------
echo "==> starting Go services"
SKIP_PREFLIGHT=1 bash scripts/run-all-services.sh

# ---- 5. Health probe -------------------------------------------------------
echo "==> probing health endpoints (60s window)"
deadline=$(( $(date +%s) + 60 ))
declare -A status_by_name
while :; do
  all_ok=true
  for i in "${!HEALTH_PORTS[@]}"; do
    port=${HEALTH_PORTS[$i]}
    name=${SERVICE_NAMES[$i]}
    code=$(curl -s -o /dev/null -w "%{http_code}" --max-time 1 "http://localhost:$port/healthz" 2>/dev/null || echo "000")
    status_by_name[$name]=$code
    if [[ "$code" != "200" ]]; then all_ok=false; fi
  done
  if $all_ok; then break; fi
  if [[ $(date +%s) -gt $deadline ]]; then break; fi
  sleep 3
done

echo ""
echo "==> service health"
ok=0; fail=0
for name in "${SERVICE_NAMES[@]}"; do
  code=${status_by_name[$name]:-000}
  if [[ "$code" == "200" ]]; then
    printf "  ✓ %-18s %s\n" "$name" "$code"
    ok=$((ok+1))
  else
    printf "  ✗ %-18s %s   (tail .run/%s.log for details)\n" "$name" "$code" "$name"
    fail=$((fail+1))
  fi
done
echo ""
echo "==> $ok healthy, $fail failed"
echo ""
echo "next: cd web && npm run dev"
echo "stop: bash scripts/dev-down.sh   (or kill .run/services.pids manually)"

#!/usr/bin/env bash
# SeDoc test-server installer — Linux / WSL2.
#
# Stands up the full docker-compose stack with ONLY docker as a host
# dependency: migrations run in a migrate/migrate container and seeding runs
# in a golang container, so Go, golang-migrate and openssl are not required.
# Idempotent — safe to re-run after fixing whatever a step complains about.
#
# Usage:
#   ./install.sh [--prebuilt] [--install-docker] [--skip-sysctl]
#
#   --prebuilt        pull ghcr.io/aieera/sedoc images where published
#                     (5 services still build from source; much faster overall)
#   --install-docker  install Docker Engine via get.docker.com when missing
#                     (needs root/sudo)
#   --skip-sysctl     skip the vm.max_map_count fix (OpenSearch will not boot
#                     below 262144 — only use this if you manage sysctls
#                     elsewhere)
#
# Env: TIMEOUT=<seconds> overrides the health-wait timeout (default 900).
#
# The Windows equivalent is install.bat (scripts/install/install.ps1).
set -euo pipefail
cd "$(dirname "$0")"

# Pin the compose project: overlays carrying their own `name:` (last-file-wins)
# would otherwise fork the stack into a second project and break the health
# wait + the network name used for dockerized migrate/seed below.
export COMPOSE_PROJECT_NAME=sedoc-dev

PREBUILT=0
INSTALL_DOCKER=0
SKIP_SYSCTL=0
for arg in "$@"; do
  case "$arg" in
    --prebuilt)       PREBUILT=1 ;;
    --install-docker) INSTALL_DOCKER=1 ;;
    --skip-sysctl)    SKIP_SYSCTL=1 ;;
    -h|--help)        awk 'NR>1 { if (!/^#/) exit; sub(/^# ?/,""); print }' "$0"; exit 0 ;;
    *) echo "unknown option: $arg (see --help)" >&2; exit 2 ;;
  esac
done

step() { echo ""; echo "==> $*"; }
fail() { echo "ERROR: $*" >&2; exit 1; }

# Read KEY=VALUE from .env without sourcing it (values may contain spaces).
# Strips a trailing CR (a .env written on Windows is CRLF) and surrounding
# double quotes, the way compose's .env parser does.
env_val() {
  local v
  v="$(grep -E "^$1=" .env 2>/dev/null | head -1 | cut -d= -f2-)" || true
  v="${v%$'\r'}"
  v="${v%\"}"
  printf '%s' "${v#\"}"
}

# Root or sudo prefix for privileged operations.
SUDO=""
if [ "$(id -u)" != "0" ]; then
  SUDO="sudo"
fi

IS_WSL=0
if grep -qi microsoft /proc/version 2>/dev/null; then IS_WSL=1; fi

# --- 1. Preflight -----------------------------------------------------------
step "1/8 Preflight: docker + compose v2"

if ! command -v docker >/dev/null 2>&1; then
  if [ "$INSTALL_DOCKER" = "1" ]; then
    echo "  docker missing — installing via get.docker.com"
    curl -fsSL https://get.docker.com | $SUDO sh
    $SUDO systemctl enable --now docker 2>/dev/null || true
  else
    fail "docker not found. Install Docker Engine + compose plugin
  (curl -fsSL https://get.docker.com | sudo sh) or re-run with --install-docker.
  On WSL2, install Docker Desktop on Windows instead and enable WSL integration."
  fi
fi

if ! docker info >/dev/null 2>&1; then
  if [ "$IS_WSL" = "1" ]; then
    fail "docker daemon unreachable. Start Docker Desktop on Windows and make sure
  'WSL integration' is enabled for this distro (Settings → Resources → WSL integration)."
  fi
  fail "docker daemon unreachable. Start it (sudo systemctl start docker) and make
  sure your user is in the docker group (sudo usermod -aG docker \$USER, then re-login)."
fi

docker compose version >/dev/null 2>&1 \
  || fail "docker compose v2 plugin missing (docker-compose v1 is not supported).
  Install docker-compose-plugin, or Docker Desktop which bundles it."

[ "$(docker info --format '{{.OSType}}' 2>/dev/null)" = "linux" ] \
  || fail "docker daemon is not in Linux-containers mode."

echo "  ok"

# --- 2. Kernel: vm.max_map_count for OpenSearch ----------------------------
step "2/8 Kernel: vm.max_map_count >= 262144 (OpenSearch bootstrap check)"

CURRENT_MMC=$(cat /proc/sys/vm/max_map_count 2>/dev/null || echo 0)
if [ "$SKIP_SYSCTL" = "1" ]; then
  echo "  skipped (--skip-sysctl); current value: $CURRENT_MMC"
elif [ "$CURRENT_MMC" -ge 262144 ]; then
  echo "  already $CURRENT_MMC — nothing to do"
else
  $SUDO sysctl -w vm.max_map_count=262144 \
    || fail "could not raise vm.max_map_count (currently $CURRENT_MMC).
  OpenSearch will exit at boot without it. Run as root or fix sudo, then re-run."
  # Persist. On classic Linux this survives reboot; on WSL2 the file only
  # helps if systemd is enabled, so also point at .wslconfig.
  if echo "vm.max_map_count=262144" | $SUDO tee /etc/sysctl.d/99-sedoc.conf >/dev/null 2>&1; then
    echo "  persisted to /etc/sysctl.d/99-sedoc.conf"
  else
    echo "  warning: could not persist; the runtime value is set for this boot only"
  fi
  if [ "$IS_WSL" = "1" ]; then
    echo "  NOTE (WSL2): to survive 'wsl --shutdown', add to %USERPROFILE%\\.wslconfig:"
    echo "    [wsl2]"
    echo "    kernelCommandLine = sysctl.vm.max_map_count=262144"
  fi
fi

# --- 3. Memory sanity -------------------------------------------------------
step "3/8 Memory check"

TOTAL_KB=$(awk '/MemTotal/ {print $2}' /proc/meminfo 2>/dev/null || echo 0)
TOTAL_GB=$((TOTAL_KB / 1024 / 1024))
if [ "$TOTAL_GB" -lt 12 ]; then
  echo "  warning: only ${TOTAL_GB}GB visible. The full stack wants ~10-12GB"
  echo "  (ClamAV ~2GB, OnlyOffice 4GB, OpenSearch heap, 17 app services)."
  if [ "$IS_WSL" = "1" ]; then
    echo "  Raise the WSL2 cap in %USERPROFILE%\\.wslconfig:  [wsl2] memory=16GB"
  fi
else
  echo "  ${TOTAL_GB}GB — ok"
fi

# --- 4. Environment ---------------------------------------------------------
step "4/8 Generating .env + web/.env (kept if they already exist)"
./scripts/gen-dev-env.sh

# gen-dev-env.sh exits early when .env already exists, so re-check the two
# invariants the stack depends on (same checks as the Windows installer):
# a non-empty gateway secret, and web/.env carrying the SAME secret.
GATEWAY="$(env_val SEDOC_GATEWAY_SECRET)"
[ -n "$GATEWAY" ] || fail ".env exists but SEDOC_GATEWAY_SECRET is empty — the stack refuses
  to boot without it. Set it (64-char hex) in .env and re-run."
if [ -f web/.env ] || [ -f web/.env.example ]; then
  [ -f web/.env ] || cp web/.env.example web/.env
  sed -i "s|^SEDOC_GATEWAY_SECRET=.*|SEDOC_GATEWAY_SECRET=$GATEWAY|" web/.env
fi

# --- 5. Stack up ------------------------------------------------------------
step "5/8 Starting the stack (first run builds/pulls images — can take a while)"

COMPOSE_FILES=(-f docker-compose.yml)
if [ "$PREBUILT" = "1" ]; then
  COMPOSE_FILES+=(-f docker-compose.prebuilt.yml)
  docker compose "${COMPOSE_FILES[@]}" pull --ignore-buildable 2>/dev/null \
    || docker compose "${COMPOSE_FILES[@]}" pull || true
fi
docker compose "${COMPOSE_FILES[@]}" up -d --build

# --- 6. Health --------------------------------------------------------------
step "6/8 Waiting for container healthchecks (cold first boot can take minutes)"
TIMEOUT="${TIMEOUT:-900}" ./scripts/wait-for-health.sh

# --- 7. Migrations + seed (dockerized) --------------------------------------
step "7/8 Database migrations + seed"

NETWORK="sedoc-dev_default"          # compose project 'sedoc-dev', default network
DB_URL="postgres://sedoc:devpassword@postgres:5432/sedoc?sslmode=disable"
MIGRATE_IMAGE="migrate/migrate:v4.17.1"

# Pre-pull so pull-progress noise never lands in the parsed `version` output
# (layer-ID lines start with digits and would fake a non-fresh DB). Tolerate
# failure: offline-with-cached-image still works.
docker pull -q "$MIGRATE_IMAGE" >/dev/null 2>&1 || true

# Same track ordering + per-service bookkeeping tables as scripts/migrate-all.sh
# (see that file for why the order is interleaved).
mig() {
  local svc="$1"; shift
  local url="$DB_URL"
  if [ "$svc" != "document" ]; then
    url="${DB_URL}&x-migrations-table=${svc}_schema_migrations"
  fi
  docker run --rm --network "$NETWORK" \
    -v "$PWD/services/$svc/migrations:/migrations:ro" \
    "$MIGRATE_IMAGE" -database "$url" -path /migrations "$@"
}

document_version() { mig document version 2>&1 | grep -oE '^[0-9]+' || true; }

if [ -z "$(document_version)" ]; then
  echo "  document: applying initial schema (000001)"
  mig document up 1
fi
echo "  intelligence: up"; mig intelligence up
echo "  document: up";     mig document up
# Keep this list in lockstep with scripts/migrate-all.sh (§4) — auth must
# run after document (its 000001 lookup functions; else sessions die at the
# 3-minute revalidation), and forgetting a track here ships a server with
# missing tables (the task-service /tasks/mine 500 incident).
for svc in auth search audit billing connector notification task; do
  echo "  ${svc}: up"; mig "$svc" up
done

echo "  seeding default tenant + admin (idempotent)"
docker run --rm --network "$NETWORK" \
  -v "$PWD:/src" -w /src/scripts/seed \
  -v sedoc-gomodcache:/go/pkg/mod \
  -e GOWORK=off \
  -e DATABASE_URL="$DB_URL" \
  -e SEED_ADMIN_EMAIL="$(env_val SEED_ADMIN_EMAIL)" \
  -e SEED_ADMIN_PASSWORD="$(env_val SEED_ADMIN_PASSWORD)" \
  -e SEED_TENANT_SLUG="$(env_val SEED_TENANT_SLUG)" \
  -e SEED_TENANT_NAME="$(env_val SEED_TENANT_NAME)" \
  -e SEED_REGION="$(env_val SEED_REGION)" \
  golang:1.25-alpine go run .

# --- 8. Smoke + summary -----------------------------------------------------
step "8/8 Smoke check"

if command -v curl >/dev/null 2>&1; then
  CODE=$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 "http://localhost:8080/" 2>/dev/null || echo "000")
  if [ "$CODE" = "000" ]; then
    echo "  warning: gateway did not answer on http://localhost:8080 — check: docker compose ps"
  else
    echo "  gateway answering on :8080 (HTTP $CODE)"
  fi
else
  echo "  curl not available — skipping the HTTP smoke check"
fi

SEED_EMAIL="$(env_val SEED_ADMIN_EMAIL)";    SEED_EMAIL="${SEED_EMAIL:-admin@acme.local}"
SEED_PASSWORD="$(env_val SEED_ADMIN_PASSWORD)"; SEED_PASSWORD="${SEED_PASSWORD:-ChangeMe!Now2026}"
SEED_SLUG="$(env_val SEED_TENANT_SLUG)";     SEED_SLUG="${SEED_SLUG:-acme}"

echo ""
echo "=========================================="
echo "  SeDoc test server is up."
echo ""
echo "  API gateway:  http://localhost:8080"
echo ""
echo "  Default admin login:"
echo "    Tenant slug:  ${SEED_SLUG}"
echo "    Email:        ${SEED_EMAIL}"
echo "    Password:     ${SEED_PASSWORD}"
echo ""
echo "  Web UI (no container — runs on the host):"
echo "    cd web && npm install && npm run dev   # http://localhost:3000"
echo ""
echo "  After a Docker/WSL restart:  make wake"
echo "  CHANGE THE ADMIN PASSWORD after first login."
echo "=========================================="

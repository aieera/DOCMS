#!/usr/bin/env bash
# Bootstrap the VaultDMS *minimal API-core* on a fresh Ubuntu EC2 instance,
# using prebuilt images from ghcr.io/aieera/docms. For API/ERP testing only —
# NOT production (runs the dev RLS-bypass posture).
#
# Usage (from the repo root, on the VM):
#   sudo bash deploy/ec2/bootstrap.sh
#
# It is safe to re-run. TLS is NOT handled here — see docs/deploy/ec2-api-check.md §6.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$REPO_ROOT"
ENV_FILE="$REPO_ROOT/.env"
IMAGE_TAG="${SEDOC_IMAGE_TAG:-main}"
CORE="postgres redis nats minio auth policy storage document gateway"
DB_URL="postgres://vaultdms:devpassword@localhost:5432/vaultdms?sslmode=disable"

log() { printf '\n\033[1;36m==> %s\033[0m\n' "$1"; }

# ---- 1. dependencies -------------------------------------------------------
log "Installing dependencies (docker, git, psql client, migrate)"
if ! command -v docker >/dev/null; then
  apt-get update -y
  apt-get install -y ca-certificates curl git postgresql-client
  curl -fsSL https://get.docker.com | sh
fi
if ! command -v migrate >/dev/null; then
  curl -fsSL https://github.com/golang-migrate/migrate/releases/download/v4.17.1/migrate.linux-amd64.tar.gz \
    | tar -xz -C /usr/local/bin migrate
fi
# seed.sh runs `go run ./scripts/seed` to insert the admin user. The
# prebuilt images bring the services but not a Go toolchain, so we
# install one here. ~120MB; only used once.
if ! command -v go >/dev/null; then
  GO_VERSION="${GO_VERSION:-1.23.4}"
  curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz" \
    | tar -xz -C /usr/local
  ln -sf /usr/local/go/bin/go /usr/local/bin/go
fi
go version

# ---- 2. .env with fresh secrets (only if missing) --------------------------
if [ ! -f "$ENV_FILE" ] || ! grep -q SEDOC_GATEWAY_SECRET "$ENV_FILE"; then
  log "Generating .env with fresh secrets"
  PUBLIC_HOST="${PUBLIC_HOST:-$(curl -fsS http://169.254.169.254/latest/meta-data/public-ipv4 2>/dev/null || echo localhost)}"
  ADMIN_PW="$(openssl rand -base64 18)"
  cat >> "$ENV_FILE" <<EOF
SEDOC_GATEWAY_SECRET=$(openssl rand -hex 32)
SEDOC_LOCAL_KEK=$(openssl rand -hex 32)
SEDOC_ESIGN_STATE_HMAC=$(openssl rand -hex 32)
SEED_ADMIN_EMAIL=admin@acme.local
SEED_ADMIN_PASSWORD=${ADMIN_PW}
SEED_TENANT_SLUG=acme
SEDOC_S3_PUBLIC_BASE=${PUBLIC_HOST}:9000
SEDOC_ALLOW_BYPASS_RLS=1
EOF
  echo ">>> SEED ADMIN PASSWORD: ${ADMIN_PW}  (save this now)"
else
  log ".env already has secrets — leaving it untouched"
fi

export SEDOC_IMAGE_TAG="$IMAGE_TAG"
COMPOSE="docker compose -f docker-compose.yml -f docker-compose.prebuilt.yml"

# ---- 3. pull + start infra -------------------------------------------------
log "Pulling core images (tag: $IMAGE_TAG)"
$COMPOSE pull $CORE
log "Starting infra (postgres, redis, nats, minio)"
$COMPOSE up -d postgres redis nats minio

# ---- 4. migrate + seed -----------------------------------------------------
log "Applying migrations + seeding admin user"
DATABASE_URL="$DB_URL" ./scripts/seed.sh

# ---- 5. start services + gateway ------------------------------------------
log "Starting services + gateway"
$COMPOSE up -d auth policy storage document gateway

log "Done. Waiting for health — check: docker compose ps"
echo "API will be on http://<this-host>:8080  (front it with Caddy for HTTPS — see docs/deploy/ec2-api-check.md §6)"
echo "Verify:  curl -s -o /dev/null -w '%{http_code}\\n' -X POST http://localhost:8080/api/v1/auth/login -H 'Content-Type: application/json' -d '{}'   # expect 401"

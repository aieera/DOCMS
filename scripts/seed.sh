#!/usr/bin/env bash
# Wait for Postgres, run migrations, insert default admin/workspace,
# print login instructions. Idempotent: re-running is safe.
set -euo pipefail

DB_URL="${DATABASE_URL:-postgres://sedoc:devpassword@localhost:15432/sedoc?sslmode=disable}"

# Honor the SEED_* values in .env (gen-env writes them there and install.sh
# passes them through) — without this, `make setup`/`make reset` silently
# seed the acme defaults no matter what the operator configured. Process
# env still wins over .env. Values may be quoted and CRLF-terminated; strip
# both, the way compose's own .env parser does.
env_val() {
  local v
  v="$(grep -E "^$1=" .env 2>/dev/null | head -1 | cut -d= -f2-)" || true
  v="${v%$'\r'}"
  v="${v%\"}"
  printf '%s' "${v#\"}"
}
if [ -f .env ]; then
  export SEED_ADMIN_EMAIL="${SEED_ADMIN_EMAIL:-$(env_val SEED_ADMIN_EMAIL)}"
  export SEED_ADMIN_PASSWORD="${SEED_ADMIN_PASSWORD:-$(env_val SEED_ADMIN_PASSWORD)}"
  export SEED_TENANT_SLUG="${SEED_TENANT_SLUG:-$(env_val SEED_TENANT_SLUG)}"
  export SEED_TENANT_NAME="${SEED_TENANT_NAME:-$(env_val SEED_TENANT_NAME)}"
  export SEED_REGION="${SEED_REGION:-$(env_val SEED_REGION)}"
fi

echo "Waiting for Postgres..."
if command -v pg_isready >/dev/null 2>&1; then
  deadline=$(( $(date +%s) + 60 ))
  while ! pg_isready -d "$DB_URL" >/dev/null 2>&1; do
    if [ "$(date +%s)" -ge "$deadline" ]; then
      echo "  Timeout waiting for Postgres at $DB_URL" >&2
      exit 1
    fi
    sleep 1
  done
else
  echo "  pg_isready not installed; sleeping 5s and proceeding"
  sleep 5
fi

if command -v migrate >/dev/null 2>&1; then
  echo "Running migrations..."
  # Every service shares the sedoc database but has its own migration track,
  # and the tracks have cross-service dependencies (e.g. document 000021
  # extends the intelligence-owned document_entities table). migrate-all.sh
  # applies them all in the correct order; see that script for the rationale.
  DATABASE_URL="$DB_URL" "$(dirname "$0")/migrate-all.sh"
else
  echo "  migrate CLI not installed; skipping migrations"
  echo "  install: brew install golang-migrate  (or: go install github.com/golang-migrate/migrate/v4/cmd/migrate@latest)"
fi

echo "Seeding default data..."
DATABASE_URL="$DB_URL" go run ./scripts/seed

SEED_EMAIL="${SEED_ADMIN_EMAIL:-admin@acme.local}"
SEED_PASSWORD="${SEED_ADMIN_PASSWORD:-ChangeMe!Now2026}"
SEED_SLUG="${SEED_TENANT_SLUG:-acme}"

echo ""
echo "=========================================="
echo "  SeDoc is ready."
echo ""
echo "  Web UI:  http://localhost:3000"
echo "  API:     http://localhost:8080"
echo ""
echo "  Default admin login:"
echo "    Tenant slug:  ${SEED_SLUG}"
echo "    Email:        ${SEED_EMAIL}"
echo "    Password:     ${SEED_PASSWORD}"
echo ""
echo "  CHANGE THIS PASSWORD IMMEDIATELY after first login."
echo "=========================================="

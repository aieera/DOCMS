#!/usr/bin/env bash
# Wait for Postgres, run migrations, insert default admin/workspace,
# print login instructions. Idempotent: re-running is safe.
set -euo pipefail

DB_URL="${DATABASE_URL:-postgres://vaultdms:devpassword@localhost:5432/vaultdms?sslmode=disable}"

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
  migrate -database "$DB_URL" -path services/document/migrations up
  # Additional service migrations land here as they ship. For now only
  # document has its own migrations; the shared 45-table schema lives
  # in 000001_initial_schema.up.sql under the document service.
  if [ -d services/search/migrations ]; then
    migrate -database "$DB_URL" -path services/search/migrations up || true
  fi
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
echo "  Web UI:  http://localhost:5173"
echo "  API:     http://localhost:8080"
echo ""
echo "  Default admin login:"
echo "    Tenant slug:  ${SEED_SLUG}"
echo "    Email:        ${SEED_EMAIL}"
echo "    Password:     ${SEED_PASSWORD}"
echo ""
echo "  CHANGE THIS PASSWORD IMMEDIATELY after first login."
echo "=========================================="

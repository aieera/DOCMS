#!/usr/bin/env bash
# Run every service's DB migration track against the shared sedoc database,
# in cross-service dependency order. Idempotent: safe to re-run.
#
# Why ordering matters
# --------------------
# All services share one Postgres database but keep independent
# golang-migrate tracks. The document service uses the default
# `schema_migrations` table; every other service uses
# `<svc>_schema_migrations`. The dependency graph between tracks is
# *interleaved*, so no single-pass "service A fully, then service B" order
# works on a fresh DB:
#
#   - intelligence migrations FK-reference `organizations`, a table created
#     by the document service's initial schema (000001_initial_schema).
#   - document migration 000021 (NER pipeline) does
#     `ALTER TABLE document_entities`, a table OWNED and created by the
#     intelligence service (000002_classify_ner).
#
# So "document then intelligence" dies at document 000021 (no
# document_entities) and "intelligence then document" dies at intelligence
# 000001 (no organizations). The correct order anchors on document's
# initial schema, which is a stable anchor (not a magic mid-track number):
#
#   1. document  -> apply ONLY 000001  (shared schema: organizations, ...)
#   2. intelligence -> latest          (creates document_entities)
#   3. document  -> latest             (000021 extends document_entities)
#   4. remaining tracks: search, audit, billing, connector, notification
#
# Step 1 runs only when the document track is fresh; on an already-migrated
# DB it is skipped so we never accidentally roll anything back.
set -euo pipefail

# Resolve DATABASE_URL: explicit env > .env > dev default. The default host
# port is 15432, matching the docker-compose "15432:5432" mapping.
if [[ -z "${DATABASE_URL:-}" && -f .env ]]; then
  DATABASE_URL="$(grep -E '^DATABASE_URL=' .env | head -1 | cut -d= -f2-)"
fi
DATABASE_URL="${DATABASE_URL:-postgres://sedoc:devpassword@localhost:15432/sedoc?sslmode=disable}"

MIGRATE="${MIGRATE:-migrate}"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# Per-service track ordering. document is intentionally first and uses the
# default migrations table (table="" -> golang-migrate default).
#   document     -> ""  (default schema_migrations)
#   <other>      -> "<svc>_schema_migrations"

mig() { # <service> <subcommand...>; document uses the default table.
  local svc="$1"; shift
  local path="$ROOT/services/$svc/migrations"
  local url="$DATABASE_URL"
  if [[ "$svc" != "document" ]]; then
    url="${DATABASE_URL}&x-migrations-table=${svc}_schema_migrations"
  fi
  "$MIGRATE" -database "$url" -path "$path" "$@"
}

document_version() { # echoes the current document track version, or "" if fresh
  mig document version 2>&1 | grep -oE '^[0-9]+' || true
}

echo ">> migrating all services (ordered for cross-service dependencies)"

# 1. Document initial schema first — but ONLY on a fresh DB, so we never
#    downgrade an existing install. `up 1` applies just 000001.
if [[ -z "$(document_version)" ]]; then
  echo ">> document: applying initial schema (000001)"
  mig document up 1
fi

# 2. Intelligence — needs organizations (from document 000001); creates
#    document_entities (needed by document 000021).
echo ">> intelligence: up"
mig intelligence up

# 3. Document — apply the rest (no-op if already current).
echo ">> document: up"
mig document up

# 4. Remaining independent tracks. auth reads document-owned tables
#    (api_keys/sessions/users/organizations), so it must stay after the
#    document track; without its 000001 lookup functions every session dies
#    at the 3-minute Redis revalidation (the "auto-logout" incident).
for svc in auth search audit billing connector notification task; do
  echo ">> ${svc}: up"
  mig "$svc" up
done

echo ">> all migrations applied."

#!/usr/bin/env bash
# Deploy-shaped smoke: fetch a preview page THROUGH THE GATEWAY (not the
# preview service directly) and exercise the internal watermark path.
# Proves the three deploy fixes end to end against the compose stack:
#   1. /api/v1/previews/* is routed by Kong to the preview upstream;
#   2. the preview API has real S3 creds (SEDOC_S3_*), so it can presign;
#   3. the document→preview watermark call authenticates (SEDOC_SERVICE_API_KEY).
#
# Run after `make docker-up` (or in the integration-tests CI job):
#   scripts/preview-gateway-smoke.sh
#
# Env (defaults target the compose stack):
#   GATEWAY_URL  (default http://localhost:8080)   — Kong public proxy
#   PREVIEW_URL  (default http://localhost:8087)   — preview API direct (internal path)
#   REDIS_URL / MINIO_*  — seeded fixtures
set -euo pipefail

GATEWAY_URL="${GATEWAY_URL:-http://localhost:8080}"
PREVIEW_URL="${PREVIEW_URL:-http://localhost:8087}"
SERVICE_KEY="${SEDOC_SERVICE_API_KEY:-dev-service-key-rotate-in-prod}"
TENANT="11111111-1111-1111-1111-111111111111"
DOC="22222222-2222-2222-2222-222222222222"
VER="33333333-3333-3333-3333-333333333333"
REGION="${SEDOC_REGION:-us-east-1}"
BUCKET="dms-${REGION}-previews"

log() { echo ">> $*"; }
fail() { echo "FAIL: $*" >&2; exit 1; }

# 1. Seed a rendered page + a preview manifest so the gateway fetch resolves.
log "seeding a page image + manifest"
tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' EXIT
# A 1x1 PNG stands in for a rendered page.
printf '\x89PNG\r\n\x1a\n' > "$tmp/page1.png"
docker compose exec -T minio sh -c "mc alias set local http://localhost:9000 minioadmin minioadmin >/dev/null 2>&1 || true; mc mb --ignore-existing local/${BUCKET} >/dev/null 2>&1 || true"
docker compose cp "$tmp/page1.png" minio:/tmp/page1.png
docker compose exec -T minio mc cp /tmp/page1.png "local/${BUCKET}/${TENANT}/${DOC}/${VER}/page1.png" >/dev/null

manifest=$(cat <<JSON
{"status":"ready","bucket":"${BUCKET}","thumbnail_key":"${TENANT}/${DOC}/${VER}/page1.png","preview_page_keys":["${TENANT}/${DOC}/${VER}/page1.png"]}
JSON
)
# manifest key + latest-version alias the routes read.
docker compose exec -T redis sh -c "redis-cli -n 3 SET 'preview:${TENANT}:${VER}' '${manifest}' >/dev/null; redis-cli -n 3 SET 'preview_latest:${TENANT}:${DOC}' '${VER}' >/dev/null"

# 2. Fetch the page THROUGH THE GATEWAY. A wired route + working S3 presign
#    returns a 302 redirect to the presigned object URL (not a 404/503).
log "GET ${GATEWAY_URL}/api/v1/previews/${DOC}/pages/1 through the gateway"
code=$(curl -s -o /dev/null -w '%{http_code}' \
  "${GATEWAY_URL}/api/v1/previews/${DOC}/pages/1?tenant_id=${TENANT}")
[ "$code" = "302" ] || fail "gateway preview fetch returned ${code}, want 302 (route wired + presign working)"
log "gateway preview fetch -> 302 (OK)"

# 3. The internal watermark path authenticates with the service key
#    (called direct, as the document service does in-cluster).
log "POST ${PREVIEW_URL}/api/v1/previews/internal/watermark/pdf with X-Service-Key"
printf '%%PDF-1.7\n1 0 obj<<>>endobj\ntrailer<<>>\n%%%%EOF' > "$tmp/in.pdf"
wcode=$(curl -s -o /dev/null -w '%{http_code}' \
  -H "X-Service-Key: ${SERVICE_KEY}" \
  -F "file=@${tmp}/in.pdf;type=application/pdf" -F "text=Alice" \
  "${PREVIEW_URL}/api/v1/previews/internal/watermark/pdf")
[ "$wcode" = "200" ] || fail "internal watermark returned ${wcode}, want 200 (service key provisioned)"
# And the SAME call WITHOUT the key must be rejected.
ncode=$(curl -s -o /dev/null -w '%{http_code}' \
  -F "file=@${tmp}/in.pdf;type=application/pdf" -F "text=Alice" \
  "${PREVIEW_URL}/api/v1/previews/internal/watermark/pdf")
[ "$ncode" = "401" ] || fail "internal watermark without a key returned ${ncode}, want 401 (fail closed)"
log "internal watermark -> 200 with key, 401 without (OK)"

echo "PASS: gateway preview fetch + internal watermark auth end to end"

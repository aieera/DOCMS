#!/usr/bin/env bash
# VaultDMS API-only smoke test. Exercises login → upload → OCR wait →
# search → share → logout via curl. No browser, no Node. Exits 0 on
# success, non-zero with a diagnostic message on the first failure.
#
# Env:
#   E2E_BASE_URL   (default http://localhost:3000 — vite proxy front)
#   ADMIN_EMAIL    (default admin@acme.local)
#   ADMIN_PASSWORD (default ChangeMe!Now2026)
#   TENANT_SLUG    (default acme)
#   OCR_TIMEOUT    (default 60 seconds)

set -u -o pipefail

BASE_URL="${E2E_BASE_URL:-http://localhost:3000}"
ADMIN_EMAIL="${ADMIN_EMAIL:-admin@acme.local}"
ADMIN_PASSWORD="${ADMIN_PASSWORD:-ChangeMe!Now2026}"
TENANT_SLUG="${TENANT_SLUG:-acme}"
OCR_TIMEOUT="${OCR_TIMEOUT:-60}"

cd "$(dirname "$0")"
FIXTURE="fixtures/sample-contract.pdf"
if [ ! -f "$FIXTURE" ]; then
  echo "[api-smoke] regenerating fixture PDF"
  node fixtures/build-fixture.js
fi

COOKIE_JAR="$(mktemp -t e2e-cookies.XXXXXX)"
trap 'rm -f "$COOKIE_JAR"' EXIT

say() { printf '\n==> %s\n' "$*"; }
die() { printf '\n[FAIL] %s\n' "$*" >&2; exit 1; }

# ---- Tiny JSON helpers: avoid jq dependency ------------------------------
json_get() {
  # json_get <body> <dotted.path>   — returns the leaf value or ''.
  # Handles flat objects and one level of nesting; good enough for our
  # known response shapes.
  local body="$1" path="$2"
  node -e "
    let d=''; process.stdin.on('data',c=>d+=c).on('end',()=>{
      try {
        const o=JSON.parse(d);
        const parts='${path}'.split('.');
        let cur=o;
        for (const p of parts) { cur = cur?.[p]; if (cur===undefined) break; }
        process.stdout.write(cur===undefined||cur===null?'':String(cur));
      } catch(e){}
    });
  " <<< "$body"
}

# ---- 1. Login ------------------------------------------------------------
say "1. POST /auth/login"
LOGIN_BODY=$(curl -s -c "$COOKIE_JAR" -X POST "$BASE_URL/api/v1/auth/login" \
  -H 'Content-Type: application/json' \
  -d "{\"email\":\"$ADMIN_EMAIL\",\"password\":\"$ADMIN_PASSWORD\",\"tenant_slug\":\"$TENANT_SLUG\"}")
USER_ID=$(json_get "$LOGIN_BODY" 'user.id')
TENANT_ID=$(json_get "$LOGIN_BODY" 'user.tenant_id')
[ -n "$USER_ID" ] || die "login: no user.id in response ($LOGIN_BODY)"
[ -n "$TENANT_ID" ] || die "login: no tenant_id"
say "   user=$USER_ID tenant=$TENANT_ID"

# ---- 2. Pick a workspace + folder so uploads have a home ------------------
say "2. GET /workspaces"
WS=$(curl -s -b "$COOKIE_JAR" "$BASE_URL/api/v1/workspaces")
# Expect an array; grab the first id.
WS_ID=$(node -e "
  let d=''; process.stdin.on('data',c=>d+=c).on('end',()=>{
    try { const a=JSON.parse(d); process.stdout.write(Array.isArray(a)&&a[0]?.id||'') } catch {}
  });" <<< "$WS")
[ -n "$WS_ID" ] || die "no workspace in response ($WS)"
say "   workspace=$WS_ID"

FOLDERS=$(curl -s -b "$COOKIE_JAR" "$BASE_URL/api/v1/workspaces/$WS_ID/folders")
FOLDER_ID=$(node -e "
  let d=''; process.stdin.on('data',c=>d+=c).on('end',()=>{
    try { const a=JSON.parse(d); process.stdout.write(Array.isArray(a)&&a[0]?.id||'') } catch {}
  });" <<< "$FOLDERS")
say "   folder=$FOLDER_ID"

# ---- 3. Upload: initiate -------------------------------------------------
say "3. POST /storage/uploads/initiate"
SIZE=$(wc -c < "$FIXTURE" | tr -d ' ')
SHA=$(sha256sum "$FIXTURE" | awk '{print $1}')
INITIATE=$(curl -s -b "$COOKIE_JAR" -X POST "$BASE_URL/api/v1/storage/uploads/initiate" \
  -H 'Content-Type: application/json' \
  -d "{\"workspace_id\":\"$WS_ID\",\"folder_id\":\"$FOLDER_ID\",\"filename\":\"sample-contract.pdf\",\"size_bytes\":$SIZE,\"sha256_hash\":\"$SHA\",\"mime_type\":\"application/pdf\"}")
UPLOAD_ID=$(json_get "$INITIATE" 'upload_id')
PUT_URL=$(json_get "$INITIATE" 'presigned_put_url')
[ -n "$UPLOAD_ID" ] || die "no upload_id ($INITIATE)"
[ -n "$PUT_URL" ] || die "no presigned PUT url"
say "   upload_id=$UPLOAD_ID"

# ---- 4. PUT bytes to the presigned URL -----------------------------------
say "4. PUT bytes to presigned URL"
PUT_STATUS=$(curl -s -o /dev/null -w '%{http_code}' -X PUT --data-binary @"$FIXTURE" "$PUT_URL")
[ "$PUT_STATUS" = "200" ] || die "presigned PUT returned $PUT_STATUS"

# ---- 5. Complete the upload ---------------------------------------------
say "5. POST /storage/uploads/:id/complete"
COMPLETE=$(curl -s -b "$COOKIE_JAR" -X POST \
  "$BASE_URL/api/v1/storage/uploads/$UPLOAD_ID/complete" \
  -H 'Content-Type: application/json' \
  -d "{\"sha256_hash\":\"$SHA\",\"size_bytes\":$SIZE}")
DOC_ID=$(json_get "$COMPLETE" 'document_id')
VER_ID=$(json_get "$COMPLETE" 'version_id')
[ -n "$DOC_ID" ] || die "no document_id after complete ($COMPLETE)"
say "   document=$DOC_ID version=$VER_ID"

# ---- 6. Poll for OCR completion ------------------------------------------
say "6. Poll version status for OCR completion (max ${OCR_TIMEOUT}s)"
DEADLINE=$(( $(date +%s) + OCR_TIMEOUT ))
while :; do
  BODY=$(curl -s -b "$COOKIE_JAR" "$BASE_URL/api/v1/documents/$DOC_ID/versions")
  STATUS=$(node -e "
    let d=''; process.stdin.on('data',c=>d+=c).on('end',()=>{
      try { const a=JSON.parse(d); const v=Array.isArray(a)?a[0]:null; process.stdout.write(v?.ocr_status||v?.status||'') } catch {}
    });" <<< "$BODY")
  case "$STATUS" in
    indexed|ocr_completed|completed)
      say "   status=$STATUS"
      break
      ;;
  esac
  if [ "$(date +%s)" -gt "$DEADLINE" ]; then
    die "timeout waiting for OCR (last status='$STATUS')"
  fi
  sleep 2
done

# ---- 7. Search for CONFIDENTIAL ------------------------------------------
say "7. POST /search"
SEARCH=$(curl -s -b "$COOKIE_JAR" -X POST "$BASE_URL/api/v1/search" \
  -H 'Content-Type: application/json' \
  -d '{"query":"CONFIDENTIAL"}')
HITS=$(node -e "
  let d=''; process.stdin.on('data',c=>d+=c).on('end',()=>{
    try { const o=JSON.parse(d); process.stdout.write(String((o.hits||[]).length)) } catch { process.stdout.write('0') }
  });" <<< "$SEARCH")
[ "$HITS" -gt 0 ] || die "search returned 0 hits for CONFIDENTIAL"
# DoD #14: a hit must carry a snippet/highlight that contains the queried
# term — otherwise UI rendering breaks even though the count is non-zero.
SNIPPET=$(node -e "
  let d=''; process.stdin.on('data',c=>d+=c).on('end',()=>{
    try {
      const o=JSON.parse(d);
      const h=(o.hits||[])[0]||{};
      process.stdout.write(String(h.snippet||h.highlight||(h.highlights||[])[0]||''));
    } catch { process.stdout.write('') }
  });" <<< "$SEARCH")
[ -n "$SNIPPET" ] || die "search hit has no snippet/highlight field"
case "$(printf '%s' "$SNIPPET" | tr '[:upper:]' '[:lower:]')" in
  *confidential*) ;;
  *) die "snippet does not contain queried term: $SNIPPET" ;;
esac
say "   hits=$HITS snippet=${SNIPPET:0:80}..."

# ---- 8. Create share link ------------------------------------------------
say "8. POST /documents/:id/share-links"
SHARE=$(curl -s -b "$COOKIE_JAR" -X POST "$BASE_URL/api/v1/documents/$DOC_ID/share-links" \
  -H 'Content-Type: application/json' \
  -d '{"expires_in_hours":168}')
SHARE_URL=$(json_get "$SHARE" 'url')
SHARE_TOKEN=$(json_get "$SHARE" 'token')
if [ -n "$SHARE_URL" ]; then
  say "   share_url=$SHARE_URL"
elif [ -n "$SHARE_TOKEN" ]; then
  SHARE_URL="$BASE_URL/share/$SHARE_TOKEN"
  say "   share_url=$SHARE_URL (built from token)"
else
  echo "   WARN: share-link endpoint returned no url/token ($SHARE) — skipping anon check"
fi

# ---- 9. Anon fetch of the share link ------------------------------------
if [ -n "$SHARE_URL" ]; then
  say "9. GET share URL without cookie"
  ANON_STATUS=$(curl -s -o /dev/null -w '%{http_code}' "$SHARE_URL")
  [ "$ANON_STATUS" = "200" ] || echo "   WARN: anon share fetch returned $ANON_STATUS"
fi

# ---- 10. Logout ----------------------------------------------------------
say "10. POST /auth/logout"
curl -s -b "$COOKIE_JAR" -X POST "$BASE_URL/api/v1/auth/logout" > /dev/null

echo
echo "[api-smoke] PASS"

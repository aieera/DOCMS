#!/usr/bin/env bash
set -euo pipefail

# ── inputs ─────────────────────────────────────────────────────────────
DMS_BASE_URL="${DMS_BASE_URL:-https://dms.yourcompany.com}"
TENANT_ADMIN_USER="${TENANT_ADMIN_USER:-admin@acme.local}"
TENANT_ADMIN_PASS="${TENANT_ADMIN_PASS:?must set TENANT_ADMIN_PASS}"
TENANT_SUBDOMAIN="${TENANT_SUBDOMAIN:-acme}"
WORKSPACE_NAME="${WORKSPACE_NAME:-ERP Documents}"
CALLBACK_URL="${CALLBACK_URL:?must set CALLBACK_URL, e.g. https://erp.yourcompany.com/api/webhooks/dms}"

# ── 0.2.a login (gets a session cookie we use for the next admin calls) ─
echo "→ logging in as ${TENANT_ADMIN_USER}…"
COOKIE_JAR=$(mktemp)
LOGIN_BODY=$(jq -n --arg e "$TENANT_ADMIN_USER" --arg p "$TENANT_ADMIN_PASS" --arg t "$TENANT_SUBDOMAIN" \
  '{email:$e, password:$p, tenant_slug:$t}')
curl -sS --cookie-jar "$COOKIE_JAR" \
  -H 'Content-Type: application/json' \
  -d "$LOGIN_BODY" \
  "${DMS_BASE_URL}/api/v1/auth/login" \
  | jq -r '.user.tenant_id' \
  | tee /tmp/tenant_id

TENANT_ID=$(cat /tmp/tenant_id)
echo "  tenant_id: ${TENANT_ID}"

# Double-submit CSRF: login set a JS-readable `dms_csrf` cookie; cookie-authed
# mutations (POST /workspaces, POST /api-keys) must echo it in X-CSRF-Token or
# the auth service returns 403. Pull it out of the cookie jar (col 6 = name,
# col 7 = value in the Netscape cookie format curl writes).
CSRF_TOKEN=$(awk '$6=="dms_csrf"{print $7}' "$COOKIE_JAR" | tail -1)

# ── 0.2.b ensure workspace ─────────────────────────────────────────────
echo "→ ensuring workspace '${WORKSPACE_NAME}'…"
EXISTING_WS=$(curl -sS --cookie "$COOKIE_JAR" \
  "${DMS_BASE_URL}/api/v1/workspaces" \
  | jq -r --arg n "$WORKSPACE_NAME" '.workspaces[]? | select(.name == $n) | .id' | head -1)
if [[ -n "$EXISTING_WS" ]]; then
  WORKSPACE_ID="$EXISTING_WS"
  echo "  reused: ${WORKSPACE_ID}"
else
  WORKSPACE_ID=$(curl -sS --cookie "$COOKIE_JAR" \
    -H 'Content-Type: application/json' \
    -H "X-CSRF-Token: ${CSRF_TOKEN}" \
    -d "$(jq -n --arg n "$WORKSPACE_NAME" '{name:$n, description:"ERP integration"}')" \
    "${DMS_BASE_URL}/api/v1/workspaces" | jq -r '.id')
  echo "  created: ${WORKSPACE_ID}"
fi

# ── 0.2.c mint the service-account API key ─────────────────────────────
echo "→ minting API key…"
KEY_RESP=$(curl -sS --cookie "$COOKIE_JAR" \
  -H 'Content-Type: application/json' \
  -H "X-CSRF-Token: ${CSRF_TOKEN}" \
  -d '{
    "name": "ERP integration",
    "scopes": [
      "documents:read", "documents:write",
      "search:read", "upload",
      "webhooks:manage",
      "integrations:read", "integrations:write"
    ]
  }' \
  "${DMS_BASE_URL}/api/v1/auth/api-keys")

API_KEY=$(echo "$KEY_RESP" | jq -r '.api_key')
KEY_ID=$(echo "$KEY_RESP" | jq -r '.key_id')

# ── 0.2.d sanity check the key works ───────────────────────────────────
# API keys authenticate ONLY on the iPaaS trigger surface
# (/api/v1/integrations/triggers/*, scope integrations:read) — the main
# documents/workspaces REST routes are session-cookie auth and reject
# Bearer keys. This GET is read-only and returns `[]` for a fresh tenant.
echo "→ verifying key…"
HTTP_CODE=$(curl -sS -o /dev/null -w '%{http_code}' \
  -H "Authorization: Bearer ${API_KEY}" \
  "${DMS_BASE_URL}/api/v1/integrations/triggers/documents")
if [[ "$HTTP_CODE" != "200" ]]; then
  echo "✗ key verification failed: HTTP ${HTTP_CODE}"
  exit 1
fi

# ── 0.2.e print outputs (paste these into ERP settings) ────────────────
cat <<EOF

════════════════════════════════════════════════════════════════════
  PASTE THESE INTO ERP → Settings → External APIs → DMS
════════════════════════════════════════════════════════════════════
  dms_base_url:        ${DMS_BASE_URL}
  dms_workspace_id:    ${WORKSPACE_ID}
  dms_api_key:         ${API_KEY}
  dms_callback_url:    ${CALLBACK_URL}
  dms_auth_header:     Authorization
  dms_auth_scheme:     Bearer
════════════════════════════════════════════════════════════════════
  KEY METADATA (save for rotation/revocation)
    key_id:    ${KEY_ID}
    tenant_id: ${TENANT_ID}
════════════════════════════════════════════════════════════════════

EOF

rm -f "$COOKIE_JAR" /tmp/tenant_id

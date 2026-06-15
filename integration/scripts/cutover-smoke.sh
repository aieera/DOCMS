#!/usr/bin/env bash
# Cutover smoke test — exercises the full ERP→SeDoc path against a LIVE ERP in a
# staging tenant: emits all four canonical events to the worker webhook (a
# document.committed carrying a real file_ref, so the live ERP render endpoint is
# hit), waits for the worker to provision + file, then via the BFF verifies the
# customer tree, a document download, and an authz denial for an unauthorized
# user (which hits the live ERP authz endpoint). Prints PASS/FAIL per step and
# exits non-zero on any failure.
#
# Defaults target a LOCAL stack + the mock ERP (zero config). For a REAL ERP in
# staging, set the values below to REAL staging identifiers — in particular
# CUSTOMER / AUTH_USER must be a customer + user the real ERP authorizes, and
# DOC_FILE_REF / ATTACH_FILE_REF must be file_refs the real ERP can render:
#
#   WORKER_WEBHOOK_URL=https://worker.staging.example.com \
#   BFF_URL=https://files.staging.example.com \
#   CUSTOMER=ACME-42 AUTH_USER=alice UNAUTH_USER=mallory \
#   DOC_FILE_REF=erpdoc-998877 ATTACH_FILE_REF=erpfile-55012 \
#     ./scripts/cutover-smoke.sh
#
# Requires: curl, jq.
set -uo pipefail

WORKER_WEBHOOK_URL=${WORKER_WEBHOOK_URL:-http://localhost:8090}
BFF_URL=${BFF_URL:-http://localhost:8091}
AUTH_USER=${AUTH_USER:-alice}
UNAUTH_USER=${UNAUTH_USER:-noauth-smoke} # mock denies "noauth-*"; set a real denied user for a real ERP
RUN=$(date +%s)
CUSTOMER=${CUSTOMER:-SMOKE-$RUN}
DOC_NUMBER=${DOC_NUMBER:-$RUN}
DOC_FILE_REF=${DOC_FILE_REF:-smoke-doc-$RUN}
ATTACH_FILE_REF=${ATTACH_FILE_REF:-smoke-attach-$RUN}
WAIT_SECS=${WAIT_SECS:-90}

WEBHOOK="$WORKER_WEBHOOK_URL/webhooks/erp"
command -v jq >/dev/null || { echo "ERROR: jq is required" >&2; exit 2; }

fails=0
ok()  { printf '  \033[32mPASS\033[0m %s\n' "$1"; }
bad() { printf '  \033[31mFAIL\033[0m %s\n' "$1"; fails=$((fails + 1)); }

# emit POSTs an event JSON to the worker webhook; asserts 202.
emit() {
  local label="$1" json="$2" code
  code=$(curl -sS -o /dev/null -w '%{http_code}' -X POST "$WEBHOOK" \
    -H 'Content-Type: application/json' -d "$json")
  if [ "$code" = "202" ]; then ok "$label → 202"; else bad "$label → ${code:-no-response} (want 202)"; fi
}

bff_get() { curl -sS -H "X-ERP-User: $AUTH_USER" "$@"; }

echo "Cutover smoke — customer=$CUSTOMER run=$RUN"
echo "  worker=$WEBHOOK  bff=$BFF_URL  authUser=$AUTH_USER  unauthUser=$UNAUTH_USER"

echo "1) emit the four canonical events"
emit "customer.created" \
  "{\"erp_event_id\":\"smoke-$RUN-created\",\"kind\":\"customer.created\",\"customer_ref\":\"$CUSTOMER\",\"name\":\"Smoke $RUN\"}"
emit "customer.updated (rename)" \
  "{\"erp_event_id\":\"smoke-$RUN-rename\",\"kind\":\"customer.updated\",\"customer_ref\":\"$CUSTOMER\",\"name\":\"Smoke $RUN (renamed)\"}"
emit "document.committed (invoice, via render)" \
  "{\"erp_event_id\":\"smoke-$RUN-doc\",\"kind\":\"document.committed\",\"customer_ref\":\"$CUSTOMER\",\"doc_type\":\"invoice\",\"doc_number\":\"$DOC_NUMBER\",\"status\":\"confirmed\",\"file_ref\":\"$DOC_FILE_REF\",\"mime\":\"application/pdf\"}"
emit "attachment.uploaded (via render → /ingest)" \
  "{\"erp_event_id\":\"smoke-$RUN-attach\",\"kind\":\"attachment.uploaded\",\"customer_ref\":\"$CUSTOMER\",\"file_ref\":\"$ATTACH_FILE_REF\",\"filename\":\"smoke-attachment.pdf\"}"

echo "2) wait for provisioning + filing (<= ${WAIT_SECS}s)"
deadline=$(( $(date +%s) + WAIT_SECS ))
invoice_folder=""
while [ "$(date +%s)" -lt "$deadline" ]; do
  invoice_folder=$(bff_get "$BFF_URL/files/customers/$CUSTOMER/tree" |
    jq -r '.folders[]? | select(.name=="Invoices") | .id' 2>/dev/null | head -1)
  [ -n "$invoice_folder" ] && [ "$invoice_folder" != "null" ] && break
  sleep 3
done
if [ -n "$invoice_folder" ] && [ "$invoice_folder" != "null" ]; then
  ok "customer tree provisioned (Invoices folder $invoice_folder)"
else
  bad "customer tree not provisioned within ${WAIT_SECS}s (worker reachable? user authorized for $CUSTOMER?)"
fi

docid=""
if [ -n "$invoice_folder" ] && [ "$invoice_folder" != "null" ]; then
  while [ "$(date +%s)" -lt "$deadline" ]; do
    docid=$(bff_get "$BFF_URL/files/customers/$CUSTOMER/folders/$invoice_folder/documents" |
      jq -r --arg ext "invoice-$DOC_NUMBER" '.documents[]? | select(.externalId==$ext) | .id' 2>/dev/null | head -1)
    [ -n "$docid" ] && [ "$docid" != "null" ] && break
    sleep 3
  done
  if [ -n "$docid" ] && [ "$docid" != "null" ]; then
    ok "invoice filed (doc $docid, external_id invoice-$DOC_NUMBER) — ERP render succeeded"
  else
    bad "invoice not filed within ${WAIT_SECS}s (ERP render endpoint reachable for $DOC_FILE_REF?)"
  fi
fi

echo "3) download the filed document (bytes originate from the ERP render endpoint)"
if [ -n "$docid" ] && [ "$docid" != "null" ]; then
  size=$(bff_get -o /dev/null -w '%{size_download}' "$BFF_URL/files/documents/$docid/download")
  if [ "${size:-0}" -gt 0 ]; then ok "download returned ${size} bytes"; else bad "download empty/failed"; fi
else
  bad "download skipped (no document to download)"
fi

echo "4) authz denial for an unauthorized user"
code=$(curl -sS -o /dev/null -w '%{http_code}' -H "X-ERP-User: $UNAUTH_USER" \
  "$BFF_URL/files/customers/$CUSTOMER/tree")
# The BFF returns 404 (not 403) for a denied user so it doesn't even confirm the
# customer exists.
if [ "$code" = "404" ]; then ok "unauthorized user → 404 (ERP authz denied)"; else bad "unauthorized user → ${code:-no-response} (want 404)"; fi

echo
if [ "$fails" -eq 0 ]; then
  echo -e "\033[32m✅ cutover smoke PASSED\033[0m"
  exit 0
else
  echo -e "\033[31m❌ cutover smoke FAILED ($fails check(s))\033[0m"
  exit 1
fi

#!/usr/bin/env bash
# Bulk-create N load-test tenants by calling the billing service's
# internal provision endpoint (POST /internal/v1/tenants/provision).
#
# Referenced by docs/load-tests/RUNBOOK.md §2.1 and wrapped by the
# k8s Job in deploy/load-test/seed-tenants.yaml so it can run inside
# the load-test cluster with cluster-DNS access to the billing svc.
#
# Why this lives next to the auth service: the runbook treats tenant
# bootstrap as part of the auth/identity bring-up, but the actual
# provision RPC is owned by billing (it's the service that creates
# the org row + admin user + subscription per
# services/billing/internal/provisioner/provisioner.go).
#
# Safety:
#   - Refuses to run unless SEDOC_LOAD_SEED_OK=1 — same guard the
#     seed.py corpus loader uses, so one missed env var blocks both
#     destructive bootstrap paths.
#   - Tenant names hard-coded to "loadtest-NNN" so the cleanup query
#     in services/billing's region_test fixtures finds them.
#
# Required env:
#   BILLING_URL        e.g. http://vaultdms-billing.vaultdms:8080
#   BILLING_API_KEY    matches the billing svc's --api-key flag
#   REGION             must match cluster_region (ADR 0110)
#
# Optional env:
#   TENANT_COUNT       default 100
#   ADMIN_PASSWORD     default a random 32-char hex (printed once)
#   PLAN               default "enterprise" (gates the §16 feature set)
#
# Exit codes:
#   0  all tenants created (or already existed — idempotent)
#   1  bad config / preflight failed
#   2  one or more provision calls failed; tenants partially created

set -euo pipefail

if [[ "${SEDOC_LOAD_SEED_OK:-}" != "1" ]]; then
  echo "Refusing: SEDOC_LOAD_SEED_OK must be 1." >&2
  echo "This script is destructive — it creates real org rows + admin users." >&2
  exit 1
fi

: "${BILLING_URL:?BILLING_URL is required (e.g. http://vaultdms-billing.vaultdms:8080)}"
: "${BILLING_API_KEY:?BILLING_API_KEY is required (matches billing svc --api-key)}"
: "${REGION:?REGION is required (must match cluster data residency)}"

TENANT_COUNT="${TENANT_COUNT:-100}"
ADMIN_PASSWORD="${ADMIN_PASSWORD:-$(openssl rand -hex 16)}"
PLAN="${PLAN:-enterprise}"

# Output dir for tenant manifest. k6 scenarios pick this up via
# tests/load/lib/config.js to choose which tenant to authenticate as.
OUT_DIR="${OUT_DIR:-/tmp/loadtest-tenants}"
mkdir -p "$OUT_DIR"
MANIFEST="$OUT_DIR/manifest.json"

echo "Creating $TENANT_COUNT tenants against $BILLING_URL (plan=$PLAN, region=$REGION)" >&2
echo "Admin password (record this — printed once): $ADMIN_PASSWORD" >&2
echo "Manifest will be written to $MANIFEST" >&2

# Wait for billing to be reachable. The Job's initContainer should
# already gate on this, but belt-and-braces — a 5s curl loop costs
# nothing and saves a confusing "Connection refused" error blast.
for i in $(seq 1 30); do
  if curl -fsS -o /dev/null -m 2 "$BILLING_URL/healthz"; then
    break
  fi
  echo "Waiting for billing service... ($i/30)" >&2
  sleep 2
done

# Probe one more time and bail with a clear message if still down.
if ! curl -fsS -o /dev/null -m 2 "$BILLING_URL/healthz"; then
  echo "ERROR: billing service not reachable at $BILLING_URL after 60s." >&2
  exit 1
fi

echo "[" > "$MANIFEST"
failed=0
created=0
first=1

for i in $(seq 1 "$TENANT_COUNT"); do
  # Zero-pad to 3 digits so manifest entries sort naturally.
  idx=$(printf "%03d" "$i")
  org_name="loadtest-${idx}"
  admin_email="admin@${org_name}.loadtest"

  body=$(cat <<JSON
{
  "org_name": "${org_name}",
  "admin_email": "${admin_email}",
  "plan": "${PLAN}",
  "region": "${REGION}",
  "data_residency_region": "${REGION}"
}
JSON
)

  # -o /dev/stdout keeps the response body; -w writes the HTTP code
  # to stderr so we can tee both. --fail-with-body returns non-zero
  # but still emits the body — exactly what we want for diagnostics.
  resp=$(curl -sS -m 30 \
    -X POST \
    -H "Content-Type: application/json" \
    -H "X-API-Key: ${BILLING_API_KEY}" \
    --data "$body" \
    -w '\n__HTTP_STATUS__%{http_code}' \
    "$BILLING_URL/internal/v1/tenants/provision" || true)

  status="${resp##*__HTTP_STATUS__}"
  body_only="${resp%__HTTP_STATUS__*}"

  if [[ "$status" == "201" ]] || [[ "$status" == "200" ]]; then
    # Append manifest entry — separator before all but first.
    if [[ $first -eq 1 ]]; then
      first=0
    else
      echo "," >> "$MANIFEST"
    fi
    # Embed enough for k6 to log in: tenant id + admin email +
    # password (since the provision endpoint inserts the admin
    # user directly, the password we send is the actual hashed
    # password). The login URL is informational.
    tenant_id=$(echo "$body_only" | grep -o '"tenant_id":"[^"]*"' | cut -d'"' -f4)
    cat >> "$MANIFEST" <<JSON
  {
    "tenant_id": "${tenant_id}",
    "org_name": "${org_name}",
    "admin_email": "${admin_email}",
    "admin_password": "${ADMIN_PASSWORD}",
    "plan": "${PLAN}",
    "region": "${REGION}"
  }
JSON
    created=$((created+1))
    # Progress: print every 10 to keep log volume manageable on
    # the 100-tenant default; 100 lines/sec spammed at kubectl is
    # painful to skim.
    if (( i % 10 == 0 )); then
      echo "  ... $i / $TENANT_COUNT created" >&2
    fi
  else
    failed=$((failed+1))
    echo "FAIL ${org_name} status=${status} body=${body_only}" >&2
    # Don't abort on individual failures — the harness needs at
    # least one tenant to be useful, and partial success is
    # recoverable. The exit code at the end reflects the count.
  fi
done

echo "" >> "$MANIFEST"
echo "]" >> "$MANIFEST"

echo "" >&2
echo "Created: $created  Failed: $failed  Manifest: $MANIFEST" >&2

if [[ $failed -gt 0 ]]; then
  echo "WARN: $failed provision calls failed — see logs above." >&2
  exit 2
fi

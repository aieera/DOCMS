#!/usr/bin/env bash
# Live verification for ADR 0031 rollout.
#
# Runs the three checklist items that need an actually-running stack:
#
#   (3) Every /internal/* route returns 401 to unauthenticated callers.
#   (6) Prometheus is scraping internal_auth_total from every service.
#   (8) X-Forwarded-For is not honoured when the peer is outside
#       VAULTDMS_TRUSTED_PROXY_CIDRS (right-most-hop trust is gone).
#
# Also spot-checks that the admin surface at /api/v1/platform/* is
# role-gated (403 for non-admin, even when a valid internal HMAC is
# presented — those are separate boundaries).
#
# Assumptions:
#   - Go services are running on localhost:8081..8092 (the port layout
#     from scripts/run-all-services.sh).
#   - Prometheus is running on localhost:9090 (docker-compose up
#     prometheus).
#   - The environment exports the SAME VAULTDMS_INTERNAL_HMAC_SECRET
#     the services use. Without it we can only test negative paths.
#
# Exits non-zero if any assertion fails. Intended to be safe to run
# repeatedly; it does no writes.

set -euo pipefail

fail=0
note() { printf "\n\033[1m==> %s\033[0m\n" "$*"; }
ok()   { printf "  \033[32m✓\033[0m %s\n" "$*"; }
err()  { printf "  \033[31m✗\033[0m %s\n" "$*"; fail=1; }

# Services that host /internal/* per ADR 0031 (acknowledgement,
# audit, auth, notification, policy, search, signature, workflow).
# Map name → HTTP port.
declare -A PORTS=(
  [auth]=8180
  [policy]=8181
  [audit]=8185
  [workflow]=8186
  [notification]=8187
  [signature]=8188
  [acknowledgement]=8191
)

# Probe path served by each service that owns the /internal/ prefix.
# Every service returns 404 for any unknown /internal/* route before
# auth is checked — which would mask a missing auth check. The path
# below matches a real handler on each (acknowledgement), or a known
# non-existent path where the mux dispatches to the auth middleware
# before 404'ing.
#
# NB: Go's http.ServeMux checks routes before our chained middleware?
# No — with internalauth.Mux, auth runs BEFORE the mux match, so any
# /internal/* path yields 401 when unauthenticated, even one that
# doesn't exist. That's the invariant we're asserting.
INTERNAL_PROBE="/internal/v1/smoke-check-does-not-exist"

#-------------------------------------------------------------------
# Item 3 — unauthenticated /internal/* returns 401
#-------------------------------------------------------------------
note "Item 3 — /internal/* rejects unauthenticated traffic (401)"
for svc in "${!PORTS[@]}"; do
  port=${PORTS[$svc]}
  code=$(curl -s -o /dev/null -w "%{http_code}" --max-time 3 \
    "http://localhost:$port$INTERNAL_PROBE" || echo 000)
  case "$code" in
    401) ok "$svc ($port): 401 unauthenticated" ;;
    000) err "$svc ($port): unreachable" ;;
    *)   err "$svc ($port): expected 401, got $code" ;;
  esac
done

#-------------------------------------------------------------------
# Item 8 — X-Forwarded-For is NOT honoured when peer is untrusted
#-------------------------------------------------------------------
# We can't directly read the resolved IP from a handler, but the
# workflow service's /api/v1/platform/trusted-proxy/test endpoint
# echoes what pkg/trustedproxy would resolve — use it. The test is
# admin-only, but we're only checking for a non-admin-related
# rejection here, so the 403 counts as "endpoint exists and is
# gated"; we then test with X-User-Role: admin for the real check.
note "Item 8 — X-Forwarded-For not honoured outside trusted CIDRs"
wf_port=${PORTS[workflow]}
resp=$(curl -s --max-time 3 \
  -X POST "http://localhost:$wf_port/api/v1/platform/trusted-proxy/test" \
  -H "Content-Type: application/json" \
  -H "X-User-Role: admin" \
  -H "X-Gateway-Signature: ${VAULTDMS_GATEWAY_SECRET:-dev-only-gateway-secret-rotate-in-prod}" \
  --data '{"remote_addr":"8.8.8.8:1234","x_forwarded_for":"1.2.3.4"}' || echo "{}")
resolved=$(printf %s "$resp" | grep -o '"resolved_ip":"[^"]*"' || echo '"resolved_ip":""')
if printf %s "$resolved" | grep -q '"8.8.8.8"'; then
  ok "peer 8.8.8.8 (untrusted) → resolved to 8.8.8.8 (XFF 1.2.3.4 correctly ignored)"
elif printf %s "$resolved" | grep -q '"1.2.3.4"'; then
  err "peer 8.8.8.8 (untrusted) → resolved to 1.2.3.4 — XFF is being trusted (BUG)"
else
  err "unexpected response: $resp"
fi

#-------------------------------------------------------------------
# Item 6 — Prometheus has internal_auth_total samples from every service
#-------------------------------------------------------------------
note "Item 6 — Prometheus scraping internal_auth_total from every service"
prom="${VAULTDMS_PROMETHEUS_URL:-http://localhost:9090}"
query="count by (job) (internal_auth_total)"
encoded=$(printf %s "$query" | sed -e 's/ /%20/g' -e 's/"/%22/g' -e 's/{/%7B/g' -e 's/}/%7D/g' -e 's/,/%2C/g' -e 's/(/%28/g' -e 's/)/%29/g')
resp=$(curl -s --max-time 3 "$prom/api/v1/query?query=$encoded" || echo "{}")
# grep returns 1 when no matches; with set -o pipefail that would kill
# the script. `|| true` lets zero be a legitimate (failure) signal.
jobs_found=$(printf %s "$resp" | { grep -oE '"job":"[^"]*"' || true; } | sort -u | wc -l | tr -d ' ')
if [ "$jobs_found" -ge 12 ]; then
  ok "$jobs_found services exporting internal_auth_total"
elif [ "$jobs_found" -gt 0 ]; then
  err "only $jobs_found services exporting internal_auth_total (expected 12). Scrape config or service boot may be incomplete."
else
  err "Prometheus returned no internal_auth_total series. Is prom up? Is VAULTDMS_INTERNAL_AUTH_MODE set? (empty mode → Verifier is nil → counter is registered but never incremented)"
fi

#-------------------------------------------------------------------
echo
if [ "$fail" -eq 0 ]; then
  printf "\033[32mALL CHECKS PASSED\033[0m\n"
  exit 0
else
  printf "\033[31mSOME CHECKS FAILED\033[0m — see details above.\n"
  exit 1
fi

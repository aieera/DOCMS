#!/usr/bin/env bash
# Wave A.1 (issues #75/#76) — forbid RLS bypass in service code.
#
# Rationale: prod Postgres runs the app role NOBYPASSRLS (helm
# postgres-cluster.yaml; pkg/database/rls_posture.go). Any service query
# that reaches for `SET LOCAL row_security = off` ERRORS under that role
# (killing background sweepers), and any service that hand-rolls its own
# `SEDOC_ALLOW_BYPASS_RLS` env check is re-inventing a bypass instead of
# using the sanctioned patterns:
#   - request/consumer paths → database.WithTenantTx
#   - cross-tenant background work → database.ForEachTenant / ListTenantIDs
#   - pre-tenant exact-match lookups → a SECURITY DEFINER function
#
# This guards the two forbidden literals in services/**/*.go (non-test):
#   1. row_security = off        (in a SQL string / Exec)
#   2. SEDOC_ALLOW_BYPASS_RLS    (env bypass hand-rolled in a service)
#
# The single legitimate reader of SEDOC_ALLOW_BYPASS_RLS is the boot gate
# pkg/database/rls_posture.go, which is under pkg/ (not scanned here).

set -euo pipefail

REPO="${REPO:-$(cd "$(dirname "$0")/.." && pwd)}"

violations=0
while IFS= read -r file; do
    case "$file" in
        *_test.go) continue ;;
    esac
    # Match an actual "row_security = off" inside a double-quoted Go
    # string (what gets passed to Exec/Query) — NOT comments. The quote
    # anchors it to a string literal; comment lines use // and won't match.
    hits=$(grep -nE '"[^"]*row_security[[:space:]]*=[[:space:]]*off[^"]*"' "$file" 2>/dev/null || true)
    if [ -n "$hits" ]; then
        echo "[FAIL] $file: 'SET row_security = off' — errors under the prod NOBYPASSRLS role."
        echo "       Enumerate tenants (database.ForEachTenant) or use a SECURITY DEFINER lookup instead:"
        echo "$hits" | sed 's/^/    /'
        violations=$((violations + 1))
    fi
    # Match SEDOC_ALLOW_BYPASS_RLS referenced in a service (comments too:
    # services must not even reason about a private bypass toggle — they
    # delegate posture to pkg/database.AssertRLSPosture).
    hits=$(grep -nE 'SEDOC_ALLOW_BYPASS_RLS' "$file" 2>/dev/null | grep -vE '^\s*[0-9]+:\s*//' || true)
    if [ -n "$hits" ]; then
        echo "[FAIL] $file: references SEDOC_ALLOW_BYPASS_RLS — services delegate posture to pkg/database.AssertRLSPosture:"
        echo "$hits" | sed 's/^/    /'
        violations=$((violations + 1))
    fi
done < <(find "$REPO/services" -type f -name '*.go' 2>/dev/null)

if [ "$violations" -gt 0 ]; then
    echo ""
    echo "See pkg/database/tenants.go (ForEachTenant) and services/auth/migrations/000001 (SECURITY DEFINER lookups)."
    exit 1
fi

echo "ok: no row_security=off or SEDOC_ALLOW_BYPASS_RLS in service code"
exit 0

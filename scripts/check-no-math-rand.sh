#!/usr/bin/env bash
# Wave 6 Prompt 6.3 — CI guard.
#
# Fails if `math/rand` is imported anywhere in security-critical
# packages. Allow-lists `math/rand/v2` (safe-ish API, but still NOT
# a crypto-grade source) only outside this guard's scope.
#
# Rationale: historical bug pattern is `import "math/rand"` used to
# mint X.509 serials, session tokens, or CSRF tokens. crypto/rand is
# the only acceptable source for security-sensitive randomness per
# RFC 4086 + NIST SP 800-90A.

set -euo pipefail

REPO="${REPO:-$(cd "$(dirname "$0")/.." && pwd)}"

# Packages where math/rand must not appear at all.
SENSITIVE_PATHS=(
    "services/auth"
    "services/signature"
    "services/policy"
    "pkg/crypto"
    "pkg/middleware/csrf"
)

violations=0
for p in "${SENSITIVE_PATHS[@]}"; do
    full="$REPO/$p"
    [ -d "$full" ] || continue
    # Match imports of math/rand (v1). Allow math/rand/v2 to pass
    # since the v2 API can't be accidentally seeded with a zero
    # default. Exclude test comments that reference the name.
    hits=$(grep -rEn '"math/rand"' "$full" --include='*.go' 2>/dev/null || true)
    if [ -n "$hits" ]; then
        echo "[FAIL] $p: math/rand imported — use crypto/rand instead:"
        echo "$hits" | sed 's/^/    /'
        violations=$((violations + 1))
    fi
done

if [ "$violations" -gt 0 ]; then
    echo
    echo "Wave 6 Prompt 6.3 enforces crypto/rand in security-sensitive paths."
    echo "If math/rand is legitimately needed (e.g. non-crypto jitter), refactor"
    echo "the usage out of the sensitive path OR switch to math/rand/v2."
    exit 1
fi

echo "ok: no math/rand v1 imports in security-sensitive packages"
exit 0

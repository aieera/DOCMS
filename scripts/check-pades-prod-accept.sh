#!/usr/bin/env bash
# T-D-7 strict gate.
#
# The services/signature/internal/pades validator regexes over raw
# PDF bytes — fine for structural CI smoke, emphatically NOT fine for
# production acceptance. Any source file marked `//go:build prod_accept`
# that imports that package means someone is plumbing the smoke check
# into a release-qualification path; fail the build before it ships.
#
# The scope is intentionally narrow: only source files that carry the
# prod_accept build tag. Regular callers (test fixtures, CI smoke) are
# unaffected.

set -euo pipefail

REPO="${REPO:-$(cd "$(dirname "$0")/.." && pwd)}"
pkg='github.com/vaultdms/vaultdms/services/signature/internal/pades'

# Find every .go file with a prod_accept build tag. Accept both the
# modern `//go:build prod_accept` and the legacy `// +build prod_accept`
# forms so a refactor to either style doesn't silently pass the gate.
mapfile -t candidates < <(grep -rlE '^//go:build[^\n]*\bprod_accept\b|^// \+build[^\n]*\bprod_accept\b' \
    --include='*.go' "$REPO" 2>/dev/null || true)

if [[ ${#candidates[@]} -eq 0 ]]; then
    echo "ok: no prod_accept files — pades validator cannot leak into release qualification"
    exit 0
fi

violations=()
for f in "${candidates[@]}"; do
    if grep -q "\"$pkg\"" "$f"; then
        violations+=("$f")
    fi
done

if [[ ${#violations[@]} -gt 0 ]]; then
    echo "FAIL: prod_accept build files import the pades smoke validator."
    echo "      The regex validator is NOT sufficient for production acceptance."
    echo "      Acceptance gates must use Adobe Reader + EU DSS (see T-D-7b)."
    echo ""
    printf '  - %s\n' "${violations[@]}"
    echo ""
    exit 1
fi

echo "ok: ${#candidates[@]} prod_accept file(s) scanned; none import the pades smoke validator"

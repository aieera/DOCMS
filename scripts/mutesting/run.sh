#!/usr/bin/env bash
# Wave 13.5 — mutation testing on security-critical packages.
#
# Spec §13.5: go-mutesting on auth, policy, storage crypto.
# Fail PR if mutation score < 70%.
#
# go-mutesting generates mutants (one-line operator flips:
# `==` → `!=`, `<` → `<=`, `+` → `-`, boolean negations, etc.),
# runs the package's tests against each mutant, and scores how
# many tests actually catch the mutation. A score of 100% means
# "every mutant we tried broke at least one test"; low scores
# mean tests coast on untested branches.
#
# We run on five packages — the surface where silent
# regressions hurt the most:
#
#   services/auth/internal/service/...             (login, MFA, sessions,
#                                                    Wave 15.3 password-change flow)
#   services/policy/internal/opa/...               (OPA evaluation)
#   services/storage/internal/service/...          (envelope encryption)
#   services/acknowledgement/internal/service/...  (Wave 15.1 HMAC attestation
#                                                    + per-campaign hash chain)
#   services/signature/internal/service/...        (Wave 15.4 saved-signature
#                                                    per-profile envelope encryption)
#
# Budget: 20 minutes per package. Beyond that the mutant set
# grows faster than the test suite can cover. The CI job has a
# 90-minute hard timeout (five × 20m = 100m worst case; expect
# most packages to finish in <5m).

set -euo pipefail

THRESHOLD="${THRESHOLD:-70}"   # minimum score % to pass
PACKAGES=(
    "github.com/vaultdms/vaultdms/services/auth/internal/service/..."
    "github.com/vaultdms/vaultdms/services/policy/internal/opa/..."
    "github.com/vaultdms/vaultdms/services/storage/internal/service/..."
    "github.com/vaultdms/vaultdms/services/acknowledgement/internal/service/..."
    "github.com/vaultdms/vaultdms/services/signature/internal/service/..."
)

if ! command -v go-mutesting >/dev/null 2>&1; then
    echo "installing go-mutesting..."
    go install github.com/avito-tech/go-mutesting/cmd/go-mutesting@latest
fi

fail=0
for pkg in "${PACKAGES[@]}"; do
    echo "=== $pkg ==="
    # --exec-timeout-in-seconds caps each mutant's test run; stops
    # a mutant that triggers an infinite loop from blocking forever.
    # --skip-without-test skips files where no test file exists at
    # all — we don't want to chase coverage into utility code that
    # genuinely doesn't need tests.
    report=$(go-mutesting \
        --exec-timeout-in-seconds=60 \
        --skip-without-test \
        "$pkg" 2>&1 || true)
    echo "$report"

    # go-mutesting prints "The mutation score is X.YZZ (killed...)"
    # as the last line when it completes. Parse that.
    score=$(echo "$report" | awk '/mutation score/ { for (i=1;i<=NF;i++) if ($i ~ /^[0-9.]+$/) { print $i; exit } }')
    if [[ -z "$score" ]]; then
        echo "!! couldn't parse mutation score for $pkg"
        fail=1
        continue
    fi

    # score is a fraction (e.g. 0.75 → 75%).
    pct=$(awk -v s="$score" 'BEGIN { printf "%.1f", s*100 }')
    echo "score: $pct% (threshold: ${THRESHOLD}%)"

    if awk -v p="$pct" -v t="$THRESHOLD" 'BEGIN { if (p+0 < t+0) exit 1; exit 0 }'; then
        echo "PASS"
    else
        echo "FAIL — below threshold"
        fail=1
    fi
done

if [[ $fail -ne 0 ]]; then
    echo ""
    echo "Mutation testing: one or more packages below ${THRESHOLD}% threshold."
    echo "Either add tests that catch the surviving mutants, or update"
    echo "the threshold with reviewer approval."
    exit 1
fi

echo ""
echo "Mutation testing: all packages >= ${THRESHOLD}%."

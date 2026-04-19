#!/usr/bin/env bash
# Wave 6 Prompt 6.4 — CI guard against context.Background() in NATS
# handler paths.
#
# Intent: every NATS message handler and worker loop must derive its
# context from the service lifecycle ctx, so SIGTERM cascades into
# in-flight work and the service drains cleanly.
#
# Strategy: NATS handler sites are identified by files that subscribe
# via nats.Subscribe(...) or by a handlerCtx/consumer helper. We
# enforce that context.Background() does NOT appear anywhere inside
# services/*/internal/service/ files that also mention `nats.`, with
# the exception of service.go's main wrapper/tests.
#
# Known legitimate uses (allowed):
#   - services/*/cmd/server/main.go — root ctx at boot + shutdown
#   - *_test.go — test fixtures
#   - services/auth/internal/service/apikey.go (bgCtx for last-used touch)
#   - services/auth/internal/service/mfa.go (kmsCtx for unwrap at static config read)

set -euo pipefail

REPO="${REPO:-$(cd "$(dirname "$0")/.." && pwd)}"

violations=0
# Files under services/*/internal/ that import nats.go must not use
# context.Background().
while IFS= read -r file; do
    # Skip test files and allow-list.
    case "$file" in
        *_test.go) continue ;;
        */services/auth/internal/service/apikey.go) continue ;;
        */services/auth/internal/service/mfa.go) continue ;;
    esac
    if ! grep -q 'nats-io/nats.go' "$file" 2>/dev/null; then
        continue
    fi
    # Allow the defensive nil-fallback pattern (`parent = context.Background()`
    # or `parent: context.Background()` in a struct literal) — those are the
    # "if caller somehow passed nil, don't deref" safety rails, not the
    # bug pattern of deriving real work from an unrooted ctx.
    hits=$(grep -n 'context\.Background()' "$file" \
        | grep -vE 'parent *[=:] *context\.Background\(\)' \
        || true)
    if [ -n "$hits" ]; then
        echo "[FAIL] $file: context.Background() inside a file that imports nats.go —"
        echo "       parent context must come from the service lifecycle (Wave 6 Prompt 6.4):"
        echo "$hits" | sed 's/^/    /'
        violations=$((violations + 1))
    fi
done < <(find "$REPO/services" -type f -name '*.go' -path '*/internal/*' 2>/dev/null)

if [ "$violations" -gt 0 ]; then
    echo
    echo "Fix by threading the service's parent ctx into the Start* function"
    echo "and deriving WithTimeout from it instead of Background()."
    exit 1
fi

echo "ok: no context.Background() in NATS-handling code paths"
exit 0

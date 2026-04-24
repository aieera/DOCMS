#!/usr/bin/env bash
# Wave 14.2 — OpenAPI drift check.
#
# Greps every HTTP handler registration across services/, normalises
# the path, and compares against the paths listed in
# docs/api/openapi.yaml. Prints:
#
#   - paths in code but not in the spec  (undocumented)
#   - paths in the spec but not in code  (stale)
#
# Exit codes:
#   0  — no drift
#   1  — undocumented or stale paths found
#   2  — tool/parse error
#
# This check is intentionally approximate. Go mux registrations vary
# (chi, gorilla, net/http, route groups with a prefix). It errs on the
# side of false positives that a human then triages — better than a
# silent green on missing docs.

set -euo pipefail

REPO_ROOT="${REPO_ROOT:-$(git rev-parse --show-toplevel 2>/dev/null || pwd)}"
SPEC="${SPEC:-$REPO_ROOT/docs/api/openapi.yaml}"
SERVICES="${SERVICES:-$REPO_ROOT/services}"

if [[ ! -f "$SPEC" ]]; then
    echo "spec not found: $SPEC" >&2
    exit 2
fi

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

# --- Routes declared in handlers ---
# Match common patterns:
#   r.Get("/foo", ...), r.Post(...), r.HandleFunc("/foo", ...)
#   mux.Handle("/foo", ...)
# Strip to the path literal.
grep -rhnE \
    '(\.(Get|Post|Put|Delete|Patch|Head|Options|Handle|HandleFunc))\s*\(\s*["`]' \
    "$SERVICES" \
    --include='*.go' \
    | grep -oE '["`][/][^"`]+["`]' \
    | tr -d '"`' \
    | sed -E 's#\{[^}]+\}#\{param\}#g' \
    | sort -u \
    > "$tmp/code-paths.txt"

# Prefix routes with /api/v1 if they don't already start with it —
# most services register at that prefix via the outer router.
awk '{ if ($0 !~ /^\/api\/v1/ && $0 !~ /^\/scim/ && $0 !~ /^\/health/ && $0 !~ /^\/metrics/) { print "/api/v1" $0 } else { print $0 } }' \
    "$tmp/code-paths.txt" \
    | sort -u > "$tmp/code-paths-prefixed.txt"

# --- Paths listed in the OpenAPI spec ---
# Naive YAML parse: lines under top-level `paths:` that look like "  /foo:".
awk '
    /^paths:/ { in_paths=1; next }
    in_paths && /^[a-zA-Z]/ { in_paths=0 }
    in_paths && /^  \// { sub(/:$/, "", $1); print $1 }
' "$SPEC" \
    | sed -E 's#\{[^}]+\}#\{param\}#g' \
    | awk '{ if ($0 !~ /^\/api\/v1/) { print "/api/v1" $0 } else { print $0 } }' \
    | sort -u > "$tmp/spec-paths.txt"

# Apply allow-list: paths listed in drift-allowlist.txt are
# GRANDFATHERED and do not fail CI. New routes added to the code
# that aren't here will. The list must shrink over time.
ALLOWLIST="${ALLOWLIST:-$REPO_ROOT/scripts/openapi/drift-allowlist.txt}"
if [[ -f "$ALLOWLIST" ]]; then
    grep -vE '^\s*(#|$)' "$ALLOWLIST" | sort -u > "$tmp/allowlist.txt"
else
    : > "$tmp/allowlist.txt"
fi

undoc=$(comm -23 "$tmp/code-paths-prefixed.txt" "$tmp/spec-paths.txt" \
        | comm -23 - "$tmp/allowlist.txt" || true)
stale=$(comm -13 "$tmp/code-paths-prefixed.txt" "$tmp/spec-paths.txt" || true)

fail=0
if [[ -n "$undoc" ]]; then
    echo "=== undocumented paths (in code, not in spec) ==="
    echo "$undoc"
    echo ""
    fail=1
fi

if [[ -n "$stale" ]]; then
    echo "=== stale paths (in spec, not in code) ==="
    echo "$stale"
    echo ""
    # Stale is a warning; code removals often outpace spec cleanup.
    # Not a hard fail unless STRICT=1.
    if [[ "${STRICT:-0}" == "1" ]]; then
        fail=1
    fi
fi

if [[ $fail -ne 0 ]]; then
    echo "OpenAPI drift detected. Update docs/api/openapi.yaml or justify in the PR." >&2
    exit 1
fi

echo "OpenAPI drift check: clean."

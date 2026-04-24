#!/usr/bin/env bash
# ADR 0032 / T-D-9 — enforce NATS subject shapes.
#
# Two accepted shapes (lowercase, dot-separated):
#
#   domain events:  dms.<aggregate>.<event>.v<n>
#   notify fan-out: dms.notify.<source>.<event>.v<n>
#
# The walk extracts every literal Go string starting with `dms.` under
# services/ and pkg/ and flags anything that matches neither shape.
# Scope excludes wildcards (`.>` / `.*`) because those are subscriber
# filters, not published subjects.
#
# Exit 1 on any violation; intended to gate CI.

set -euo pipefail

REPO="${REPO:-$(cd "$(dirname "$0")/.." && pwd)}"

# Allow-list a few non-versioned subjects that predate this ADR and
# whose owners have existing tech-debt tickets. Kept short and
# reviewable — do not extend without an ADR reference.
#
# Format: one POSIX regex per line, anchored.
ALLOW_FILE="$REPO/scripts/nats-subjects-allowlist.txt"
allowed() {
    [[ -f "$ALLOW_FILE" ]] || return 1
    local s="$1"
    while IFS= read -r pat; do
        [[ -z "$pat" || "$pat" =~ ^# ]] && continue
        if [[ "$s" =~ $pat ]]; then
            return 0
        fi
    done < "$ALLOW_FILE"
    return 1
}

# Regex for acceptable shapes — Bash's `=~` uses ERE.
domain_re='^dms\.([a-z][a-z0-9_]*\.)+v[0-9]+$'
notify_re='^dms\.notify\.([a-z][a-z0-9_]*\.)+v[0-9]+$'

# Subscriber filters (not published subjects) — skip. Three forms:
#   (1) wildcards: dms.annotation.>, dms.*.created.v1
#   (2) trailing-dot prefixes: dms.apikey. (used with HasPrefix)
#   (3) aggregate-only prefixes: dms.document (no trailing segments)
# Only complete publish subjects (ending in .v<n>) get shape-graded.
is_wildcard()     { [[ "$1" == *'.>'* || "$1" == *'.*'* || "$1" == *'.*' ]]; }
is_prefix()       { [[ "$1" == *'.' ]]; }
has_version_tail(){ [[ "$1" =~ \.v[0-9]+$ ]]; }

# Extract every `dms.xxx` literal. The sed pulls the quoted string
# content; grep strips leading/trailing non-subject chars. Covers both
# Go raw-string (`dms.x.y.v1`) and double-quoted ("dms.x.y.v1") forms.
mapfile -t hits < <(
    grep -rhoE '"dms\.[a-z0-9_.>*-]+"' \
        "$REPO/services" "$REPO/pkg" \
        --include='*.go' 2>/dev/null \
    | sed -e 's/^"//' -e 's/"$//' \
    | sort -u
)

violations=()
for s in "${hits[@]}"; do
    # Non-published forms — wildcards, trailing-dot prefixes, and
    # aggregate-only prefixes all pass without shape grading. Shape
    # is enforced only on strings that look like a complete publish
    # subject (contain a version tail).
    if is_wildcard "$s" || is_prefix "$s" || ! has_version_tail "$s"; then
        continue
    fi
    if [[ "$s" =~ $domain_re ]] || [[ "$s" =~ $notify_re ]]; then
        continue
    fi
    if allowed "$s"; then
        continue
    fi
    violations+=("$s")
done

if [[ ${#violations[@]} -gt 0 ]]; then
    echo "ADR 0032 violation: NATS subjects don't match an accepted shape."
    echo ""
    echo "Accepted shapes:"
    echo "    dms.<aggregate>.<event>.v<n>           (domain events)"
    echo "    dms.notify.<source>.<event>.v<n>       (notification fan-out)"
    echo ""
    echo "Offending subjects (edit the call site or, if legitimately grandfathered,"
    echo "add the exact string to scripts/nats-subjects-allowlist.txt with an ADR ref):"
    printf '  - %s\n' "${violations[@]}"
    exit 1
fi

echo "ok: $(wc -l <<<"${hits[*]}" | tr -d ' ') subject literal(s) scanned; all match ADR 0032 shape"

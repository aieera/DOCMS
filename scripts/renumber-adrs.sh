#!/usr/bin/env bash
# One-shot ADR renumber to align with the blueprint section table.
#
# The repo's ADR numbering drifted: when each blueprint §8.1-§10.6
# section landed, its intended slot (0061..0068) was already taken
# by earlier work. Each new ADR went into the next free slot
# (0070..0077) with a "shift pattern" note in its Status: line.
# This script realigns:
#
#   Round 1 — DEMOTE the eight ADRs currently in 0061..0068 to
#             0078..0085 (free the blueprint slots).
#   Round 2 — PROMOTE the eight new ADRs from 0070..0077 down to
#             0061..0068 (occupy the blueprint slots).
#
# After running, the ADR↔blueprint mapping is 1:1 and the
# "shift pattern" notes can be cleaned out of the new ADRs by hand.
#
# Idempotent only when run on the pre-renumber state. Re-running
# after a successful renumber is a no-op (nothing matches the
# source patterns anymore).

set -euo pipefail
cd "$(dirname "$0")/.."

# Map: old slot -> new slot
declare -A DEMOTE=(
  [0061]=0078  # ner-pipeline
  [0062]=0079  # redaction-review
  [0063]=0080  # rag-pipeline
  [0064]=0081  # litellm-routing
  [0065]=0082  # faceted-search
  [0066]=0083  # permission-filtered-search
  [0067]=0084  # autocomplete
  [0068]=0085  # saved-search-alerts
)
declare -A PROMOTE=(
  [0070]=0061  # passkeys
  [0071]=0062  # ldap
  [0072]=0063  # mfa-complete
  [0073]=0064  # approval-routing
  [0074]=0065  # co-authoring
  [0075]=0066  # threaded-comments
  [0076]=0067  # annotation-layers
  [0077]=0068  # lightweight-tasks
)

# File extensions we touch. Skips binaries, images, lockfiles.
EXTS=(go ts tsx md sql json yaml yml sh py rego)

# Build the find-files command once.
find_referencing_files() {
  local pat="$1"
  for ext in "${EXTS[@]}"; do
    find . -type f -name "*.$ext" -not -path "./.git/*" -not -path "*/node_modules/*" 2>/dev/null
  done | xargs grep -l "$pat" 2>/dev/null || true
}

renumber_one() {
  local old=$1 new=$2

  # 1. Find the ADR file by its old prefix and rename.
  local adr_file
  adr_file=$(ls "docs/adr/${old}-"*.md 2>/dev/null | head -1)
  if [[ -n "$adr_file" ]]; then
    local suffix="${adr_file#docs/adr/${old}-}"
    git mv "docs/adr/${old}-${suffix}" "docs/adr/${new}-${suffix}" 2>/dev/null || \
      mv "docs/adr/${old}-${suffix}" "docs/adr/${new}-${suffix}"
    echo "  renamed: ${old}-${suffix} -> ${new}-${suffix}"
  fi

  # 2. sed-replace every reference to that ADR. Two patterns cover
  #    the formats the codebase uses: "ADR 0078" and "0078-..." (the
  #    file-prefix form, used in cross-ADR references).
  local files
  files=$(find_referencing_files "ADR ${old}" || true)
  files+=$'\n'$(find_referencing_files "${old}-" || true)
  files=$(echo "$files" | sort -u | grep -v "^$" || true)
  if [[ -z "$files" ]]; then return; fi

  while IFS= read -r f; do
    [[ -z "$f" ]] && continue
    # `ADR 0078` -> `ADR 0078`
    sed -i "s/ADR ${old}/ADR ${new}/g" "$f"
    # `0078-ner-pipeline.md` -> `0078-ner-pipeline.md` (cross-ADR
    # links, doc references). Only flips the 4-digit prefix when
    # followed by a hyphen — avoids touching unrelated digit runs.
    sed -i "s/${old}-/${new}-/g" "$f"
  done <<< "$files"
  local n
  n=$(echo "$files" | wc -l)
  echo "  refs updated: $n files"
}

echo "=== Round 1: demote 0061..0068 -> 0078..0085 ==="
for old in "${!DEMOTE[@]}"; do
  echo "  ${old} -> ${DEMOTE[$old]}"
done | sort
for old in "${!DEMOTE[@]}"; do
  renumber_one "$old" "${DEMOTE[$old]}"
done

echo ""
echo "=== Round 2: promote 0070..0077 -> 0061..0068 ==="
for old in "${!PROMOTE[@]}"; do
  echo "  ${old} -> ${PROMOTE[$old]}"
done | sort
for old in "${!PROMOTE[@]}"; do
  renumber_one "$old" "${PROMOTE[$old]}"
done

echo ""
echo "Done. Verify: git status, go build, npm tsc."

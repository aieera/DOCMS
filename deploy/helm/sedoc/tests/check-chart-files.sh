#!/usr/bin/env bash
# C-1 — the chart ships real COPIES of three repo files under files/
# (symlinks are lost by `helm package` from a Windows checkout or a
# symlink-dropping archive). This check fails when a copy drifts from its
# source of truth; fix with --sync (copies source -> chart).
#
# Usage: deploy/helm/sedoc/tests/check-chart-files.sh [--sync]
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
chart="$(cd "$here/.." && pwd)"
repo="$(cd "$chart/../../.." && pwd)"

# chart copy               source of truth
pairs=(
  "files/kong.yaml                deploy/gateway/kong.yaml"
  "files/gateway-entrypoint.sh    deploy/gateway/entrypoint.sh"
  "files/soc2-evidence.sh         scripts/soc2-evidence.sh"
)

rc=0
for pair in "${pairs[@]}"; do
  read -r copy src <<<"$pair"
  if [ -L "$chart/$copy" ]; then
    echo "FAIL $copy is a symlink; it must be a regular copy of $src"
    rc=1
    continue
  fi
  if [ "${1:-}" = "--sync" ]; then
    cp "$repo/$src" "$chart/$copy"
    echo "synced $copy <- $src"
  elif ! cmp -s "$repo/$src" "$chart/$copy"; then
    echo "FAIL $copy differs from $src — run $0 --sync"
    rc=1
  else
    echo "ok   $copy == $src"
  fi
done
exit "$rc"

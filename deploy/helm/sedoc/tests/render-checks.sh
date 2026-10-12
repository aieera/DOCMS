#!/usr/bin/env bash
# Render the chart under every supported values combination and run the
# structural checks in check_rendered.py on each (ingress backends, secret
# alignment with ExternalSecrets, restricted securityContext, pod labels,
# migration-credential isolation). Exits non-zero on the first failure.
#
# Usage: deploy/helm/sedoc/tests/render-checks.sh
# Requires: helm 3, python3 + PyYAML.
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
chart="$(cd "$here/.." && pwd)"
repo="$(cd "$chart/../../.." && pwd)"

command -v helm >/dev/null || { echo "helm not on PATH" >&2; exit 2; }
if [ ! -d "$chart/charts" ] || [ -z "$(ls -A "$chart/charts" 2>/dev/null)" ]; then
  helm dependency build "$chart" >/dev/null
fi

all_jobs=(--set soc2Evidence.enabled=true --set auditVerify.enabled=true
          --set retentionSweep.enabled=true --set recordsCutoffSweep.enabled=true)

run() {
  local label="$1"; shift
  echo "== $label"
  # values-ci.yaml supplies the two required DEC-5 CIDRs (no defaults).
  helm template sedoc "$chart" -f "$here/values-ci.yaml" "$@" | python3 "$here/check_rendered.py"
}

run "defaults"
run "externalSecrets" --set externalSecrets.enabled=true
run "all jobs + externalSecrets" --set externalSecrets.enabled=true "${all_jobs[@]}"
run "on-prem" -f "$chart/values-onprem.yaml" --set externalSecrets.enabled=true
run "air-gapped" -f "$chart/values-airgapped.yaml"
run "uae-central overlay" -f "$repo/deploy/regions/uae-central.yaml" --set externalSecrets.enabled=true
echo "all render checks passed"

#!/usr/bin/env bash
# C-1 — assert the chart's Ingress routes only to the Kong gateway and the
# web app. Exits non-zero on any other backend (or a backend whose Service
# is not rendered).
#
# Usage:
#   deploy/helm/sedoc/tests/assert-ingress-backends.sh [helm template args...]
#       renders the chart with helm (extra args are passed through, e.g.
#       --set externalSecrets.enabled=true) and checks the result;
#   helm template sedoc deploy/helm/sedoc | deploy/helm/sedoc/tests/assert-ingress-backends.sh -
#       checks an already rendered manifest stream from stdin.
# Requires python3 + PyYAML; helm unless "-" is given.
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
chart="$(cd "$here/.." && pwd)"

if [ "${1:-}" = "-" ]; then
  exec python3 "$here/check_rendered.py" ingress-backends
fi

command -v helm >/dev/null || { echo "helm not on PATH (or pipe a rendered chart and pass '-')" >&2; exit 2; }
if [ ! -d "$chart/charts" ] || [ -z "$(ls -A "$chart/charts" 2>/dev/null)" ]; then
  helm dependency build "$chart" >/dev/null
fi
# values-ci.yaml supplies the two required DEC-5 CIDRs; later args win.
helm template sedoc "$chart" -f "$here/values-ci.yaml" "$@" | python3 "$here/check_rendered.py" ingress-backends

#!/usr/bin/env bash
# Helm golden: the preview deployment must render the S3 creds under the
# names the service actually reads (SEDOC_S3_*, NOT the old SEDOC_MINIO_*),
# plus SEDOC_SERVICE_API_KEY and SEDOC_PREVIEW_URL. Guards the regression
# where the chart injected SEDOC_MINIO_* that the Python boto3 client never
# read (nil creds in cluster).
#
# Renders in isolation (stubbed subchart deps + only the preview templates)
# so unrelated, pre-existing chart-completeness bugs don't block this check.
#
# Usage: deploy/helm/sedoc/tests/preview-env-golden.sh
# Requires: helm 3.x on PATH.
set -euo pipefail

command -v helm >/dev/null || { echo "SKIP: helm not on PATH"; exit 0; }

repo_root="$(cd "$(dirname "$0")/../../../.." && pwd)"
chart_src="$repo_root/deploy/helm/sedoc"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

cp -r "$chart_src" "$work/chart"
mkdir -p "$work/chart/charts"
for d in postgresql cloudnative-pg redis opensearch nats qdrant temporal; do
  mkdir -p "$work/chart/charts/$d"
  printf 'apiVersion: v2\nname: %s\nversion: 0.0.0\n' "$d" > "$work/chart/charts/$d/Chart.yaml"
done
# Keep only the helper .tpl files + the preview templates.
find "$work/chart/templates" -mindepth 1 -maxdepth 1 \
  ! -name '_*.tpl' ! -name 'preview' -exec rm -rf {} +

rendered="$(helm template sedoc "$work/chart" \
  --show-only templates/preview/deployment.yaml)"

fail=0
require() { grep -q "name: $1" <<<"$rendered" || { echo "MISSING env: $1"; fail=1; }; }
forbid()  { if grep -q "name: $1" <<<"$rendered"; then echo "UNEXPECTED env (mis-key regression): $1"; fail=1; fi; }

# The service reads these (pydantic SEDOC_ prefix + s3_* fields).
require SEDOC_S3_ENDPOINT
require SEDOC_S3_ACCESS_KEY
require SEDOC_S3_SECRET_KEY
require SEDOC_S3_USE_SSL
# Service-to-service auth for the document→preview watermark call.
require SEDOC_SERVICE_API_KEY
require SEDOC_PREVIEW_URL
# The old mis-keyed names must be gone — the boto3 client never read them.
forbid SEDOC_MINIO_ENDPOINT
forbid SEDOC_MINIO_ACCESS_KEY
forbid SEDOC_MINIO_SECRET_KEY

if [ "$fail" -ne 0 ]; then
  echo "FAIL: preview env golden"
  echo "--- rendered env ---"; grep 'name: SEDOC_' <<<"$rendered" || true
  exit 1
fi
echo "PASS: preview deployment renders SEDOC_S3_* + SEDOC_SERVICE_API_KEY (no SEDOC_MINIO_*)"

#!/usr/bin/env bash
# Wave 15 / Phase 1.7 — forbid MinIO-specific identifiers in prod
# code paths.
#
# Policy: MinIO is a dev convenience (docker-compose local + CI test
# backend). Any reference to `MinIOEndpoint`, `VAULTDMS_MINIO_`, or
# the literal `minio:9000` in services/, pkg/, or deploy/helm/ is a
# leak of dev-backend naming into prod paths — production uses AWS
# S3 and the code path must use the generic `S3*` names.
#
# Allow-listed locations (MinIO naming LEGITIMATELY belongs):
#   - docker-compose.yml / compose-*.yml     (MinIO container boot)
#   - .env.local.example                     (dev template)
#   - scripts/switch-s3-backend.sh           (references .env templates)
#   - any docs/ (narrative — MinIO as a concept)
#   - scripts/check-no-minio-leaks.sh        (this file)

set -euo pipefail

REPO="${REPO:-$(cd "$(dirname "$0")/.." && pwd)}"
PATTERNS='MinIOEndpoint|MinIOAccessKey|MinIOSecretKey|MinIOUseSSL|VAULTDMS_MINIO_|minio:9000'
# grep -E across the three scopes, allow-listing compose + this file.
violations=$(
    grep -rnE "$PATTERNS" \
        "$REPO/services" "$REPO/pkg" "$REPO/deploy/helm" \
        --exclude-dir=node_modules \
        --exclude-dir=__pycache__ \
        --exclude='*.pyc' \
    | grep -vE '^[^:]+/docs/' \
    | grep -vE '/check-no-minio-leaks\.sh:' \
    || true
)

if [[ -n "$violations" ]]; then
    echo "MinIO naming leaked into prod code paths. Rename to S3*:"
    echo ""
    echo "$violations"
    echo ""
    echo "Allowed locations: docker-compose.yml, .env.local.example,"
    echo "scripts/switch-s3-backend.sh, docs/**. See"
    echo "scripts/check-no-minio-leaks.sh for the policy rationale."
    exit 1
fi

echo "ok: no MinIO-specific identifiers in prod paths"
exit 0

#!/usr/bin/env bash
# Wave 14.5 — build an offline install bundle for air-gapped deploys.
#
# Produces a tarball containing:
#   - All SeDoc container images (multi-arch, saved via `docker save`)
#   - All third-party dependency images (postgres, redis, nats,
#     opensearch, minio, temporal) at their pinned versions
#   - The Helm chart + values-airgapped.yaml
#   - A load script that `docker load`s images and re-tags them to
#     the customer's private registry
#   - The install runbook
#
# Usage:
#   VERSION=1.0.0 bash scripts/airgap/build-bundle.sh
# Produces:
#   dist/vaultdms-airgap-1.0.0.tar.gz
#
# Images are pulled with `docker pull --platform=linux/amd64` — arm64
# bundles go through the same script with PLATFORM=linux/arm64.

set -euo pipefail

VERSION="${VERSION:?set VERSION, e.g. 1.0.0}"
PLATFORM="${PLATFORM:-linux/amd64}"
OUT_DIR="${OUT_DIR:-dist}"
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

REGISTRY="${REGISTRY:-ghcr.io/vaultdms}"

# --- Service images ---
SERVICES=(
    auth audit billing collaboration connector document intelligence
    notification policy preview search signature signature-signer
    storage workflow web
)

# --- Pinned third-party images ---
# Bump these together with Helm chart minor bumps. Pinning at the digest
# is the release-engineering job (Wave 14.6); here we pin at tag.
THIRDPARTY=(
    "postgres:16.2-alpine"
    "redis:7.2-alpine"
    "nats:2.10.11-alpine"
    "opensearchproject/opensearch:2.13.0"
    "minio/minio:RELEASE.2024-03-30T09-41-56Z"
    "temporalio/auto-setup:1.22.5"
    "temporalio/admin-tools:1.22.5"
)

mkdir -p "$WORK/images"
echo "=== pulling + saving service images ==="
for svc in "${SERVICES[@]}"; do
    img="${REGISTRY}/${svc}:${VERSION}"
    echo "  $img"
    docker pull --platform="$PLATFORM" "$img"
    docker save "$img" -o "$WORK/images/svc-${svc}.tar"
done

echo "=== pulling + saving third-party images ==="
for img in "${THIRDPARTY[@]}"; do
    echo "  $img"
    docker pull --platform="$PLATFORM" "$img"
    safe=$(echo "$img" | tr '/:' '__')
    docker save "$img" -o "$WORK/images/tp-${safe}.tar"
done

# --- Chart ---
echo "=== copying Helm chart ==="
mkdir -p "$WORK/chart"
cp -r deploy/helm/vaultdms "$WORK/chart/"

# --- Load script ---
cat > "$WORK/load.sh" <<'LOADEOF'
#!/usr/bin/env bash
# Load bundled images into the local Docker daemon and push to the
# customer's private registry.
#
# Usage:
#   PRIVATE_REGISTRY=registry.customer.internal/vaultdms bash load.sh
set -euo pipefail
: "${PRIVATE_REGISTRY:?set PRIVATE_REGISTRY, e.g. registry.example.com/vaultdms}"

for t in images/*.tar; do
    echo "=== loading $t ==="
    loaded=$(docker load -i "$t" | awk '/Loaded image/ { print $NF }')
    for img in $loaded; do
        # strip original registry prefix and re-tag to private registry
        # e.g. ghcr.io/vaultdms/auth:1.0.0 -> registry.customer/vaultdms/auth:1.0.0
        base=$(echo "$img" | awk -F'/' '{ print $NF }')
        new="${PRIVATE_REGISTRY}/${base}"
        docker tag "$img" "$new"
        docker push "$new"
        echo "    pushed $new"
    done
done
echo "done. now:"
echo "  helm install vaultdms chart/vaultdms -f chart/vaultdms/values-airgapped.yaml \\"
echo "      --set image.registry=${PRIVATE_REGISTRY}"
LOADEOF
chmod +x "$WORK/load.sh"

# --- Runbook ---
cp docs/deploy/airgap-install.md "$WORK/README.md" 2>/dev/null || \
    echo "(install runbook not yet written — see docs/deploy/airgap-install.md once added)" \
    > "$WORK/README.md"

# --- Pack ---
mkdir -p "$OUT_DIR"
OUT="$OUT_DIR/vaultdms-airgap-${VERSION}.tar.gz"
echo "=== packing $OUT ==="
tar -czf "$OUT" -C "$WORK" .

SIZE=$(du -h "$OUT" | cut -f1)
echo ""
echo "bundle built: $OUT ($SIZE)"
echo "SHA256: $(sha256sum "$OUT" | cut -d' ' -f1)"

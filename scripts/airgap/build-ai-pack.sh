#!/usr/bin/env bash
# Wave 14.5 — AI model-weights pack for air-gapped deploys.
#
# Produces a SEPARATE tarball from the core vaultdms-airgap bundle so
# customers who don't use AI features can skip the ~14 GB download.
# Contents:
#   - Surya OCR weights      (~4 GB, from the Surya release on HF)
#   - sentence-transformers all-MiniLM-L6-v2 (~90 MB)
#   - One self-hosted LLM (default: Llama 3.1 8B instruct, ~10 GB GGUF)
#
# The core bundle's `dms-installer install` command detects the
# ai-pack at install time; if absent, intelligence-worker deploys
# with `VAULTDMS_AI_MODE=disabled` and the RAG/classify/NER paths
# return 501 Not Implemented until the pack is loaded.
#
# Usage:
#   VERSION=1.0.0 bash scripts/airgap/build-ai-pack.sh
# Produces:
#   dist/vaultdms-ai-pack-1.0.0.tar.gz
#
# Signing: release.yml runs `cosign sign-blob` on the tarball +
# attaches the signature alongside.

set -euo pipefail

VERSION="${VERSION:?set VERSION, e.g. 1.0.0}"
OUT_DIR="${OUT_DIR:-dist}"
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

# Model manifest — pinned versions. Bumping a model bumps the pack's
# semver minor (major if the output embedding dim changes).
declare -A MODELS=(
    [surya-ocr]="https://huggingface.co/vikp/surya_rec2/resolve/main/model.safetensors"
    [surya-det]="https://huggingface.co/vikp/surya_det3/resolve/main/model.safetensors"
    [embed-minilm]="https://huggingface.co/sentence-transformers/all-MiniLM-L6-v2/resolve/main/pytorch_model.bin"
    [llm-llama31-8b-q4]="https://huggingface.co/bartowski/Meta-Llama-3.1-8B-Instruct-GGUF/resolve/main/Meta-Llama-3.1-8B-Instruct-Q4_K_M.gguf"
)

# Configure mirror if the customer has an internal HuggingFace proxy.
HF_MIRROR="${HF_MIRROR:-}"
rewrite() {
    if [[ -n "$HF_MIRROR" ]]; then
        echo "${1/https:\/\/huggingface.co/$HF_MIRROR}"
    else
        echo "$1"
    fi
}

pack_dir="$WORK/vaultdms-ai-pack-$VERSION"
mkdir -p "$pack_dir/models"

for name in "${!MODELS[@]}"; do
    url=$(rewrite "${MODELS[$name]}")
    echo ">> fetching $name from $url"
    curl -fsSL -o "$pack_dir/models/$name.bin" "$url"
done

# Manifest — operator + the installer can verify what's in the pack
# without unpacking every file.
cat > "$pack_dir/MANIFEST.json" <<EOF
{
  "version": "$VERSION",
  "created_at": "$(date -u +%Y-%m-%dT%H:%M:%SZ)",
  "models": {
$(for name in "${!MODELS[@]}"; do
    f="$pack_dir/models/$name.bin"
    size=$(stat -c%s "$f" 2>/dev/null || stat -f%z "$f")
    sha=$(sha256sum "$f" 2>/dev/null | cut -d' ' -f1 || shasum -a 256 "$f" | cut -d' ' -f1)
    echo "    \"$name\": {\"size_bytes\": $size, \"sha256\": \"$sha\"},"
done | sed '$ s/,$//')
  }
}
EOF

mkdir -p "$OUT_DIR"
tar czf "$OUT_DIR/vaultdms-ai-pack-$VERSION.tar.gz" -C "$WORK" "vaultdms-ai-pack-$VERSION"
echo
echo "Built $OUT_DIR/vaultdms-ai-pack-$VERSION.tar.gz"
echo "Size: $(du -h "$OUT_DIR/vaultdms-ai-pack-$VERSION.tar.gz" | cut -f1)"
echo
echo "Next: cosign sign-blob \"$OUT_DIR/vaultdms-ai-pack-$VERSION.tar.gz\" > \"$OUT_DIR/vaultdms-ai-pack-$VERSION.tar.gz.sig\""

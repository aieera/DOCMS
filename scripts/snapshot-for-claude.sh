#!/usr/bin/env bash
# Build a curated zip of the DMS codebase for upload to claude.ai Projects.
# Excludes secrets, deps, build artifacts, and binary assets.

set -euo pipefail

REPO_ROOT="${REPO_ROOT:-C:/Users/dell/Documents/Raabyt/DMS}"
OUT_DIR="${OUT_DIR:-C:/Users/dell/Documents/DOCMS/snapshots}"
STAMP="$(date +%Y%m%d-%H%M%S)"
OUT_ZIP="$OUT_DIR/dms-snapshot-$STAMP.zip"
STAGE="$OUT_DIR/.stage-$STAMP"

mkdir -p "$OUT_DIR" "$STAGE"

cd "$REPO_ROOT"

# Include: source, configs, docs, migrations, proto, compose files.
# Exclude: node_modules, vendor, build outputs, envs, secrets, binaries.
INCLUDE_PATTERNS=(
  '*.go' '*.ts' '*.tsx' '*.js' '*.jsx'
  '*.proto' '*.sql' '*.yaml' '*.yml' '*.toml' '*.json'
  '*.md' 'Dockerfile*' 'Makefile' '.dockerignore'
)

EXCLUDE_DIRS=(
  'node_modules' 'vendor' 'dist' 'build' '.next' '.turbo'
  '.git' '.idea' '.vscode' 'coverage' 'tmp' 'bin'
)

EXCLUDE_FILES=(
  '*.env' '.env*' '*.key' '*.pem' '*.crt' '*.p12'
  '*secret*' '*credential*' 'package-lock.json' 'yarn.lock'
  'go.sum' 'pnpm-lock.yaml'
)

# Build find expression
FIND_ARGS=(.)
for d in "${EXCLUDE_DIRS[@]}"; do
  FIND_ARGS+=(-name "$d" -prune -o)
done
FIND_ARGS+=(-type f)

# Collect candidate files
TMP_LIST="$STAGE/files.txt"
: > "$TMP_LIST"
for pat in "${INCLUDE_PATTERNS[@]}"; do
  find "${FIND_ARGS[@]}" -name "$pat" -print >> "$TMP_LIST" 2>/dev/null || true
done

# Apply file-level excludes
for pat in "${EXCLUDE_FILES[@]}"; do
  grep -v -i -- "$pat" "$TMP_LIST" > "$TMP_LIST.new" || true
  mv "$TMP_LIST.new" "$TMP_LIST"
done

# Dedupe + sort
sort -u "$TMP_LIST" -o "$TMP_LIST"

COUNT=$(wc -l < "$TMP_LIST")
echo "Staging $COUNT files..."

# Copy preserving structure
while IFS= read -r f; do
  dest="$STAGE/$f"
  mkdir -p "$(dirname "$dest")"
  cp "$f" "$dest"
done < "$TMP_LIST"

# Sanity scan for leaked secrets before zipping
echo "Scanning for secret-like strings..."
if grep -rIEn '(ghp_[A-Za-z0-9]{20,}|AKIA[0-9A-Z]{16}|-----BEGIN [A-Z ]*PRIVATE KEY-----)' "$STAGE" 2>/dev/null; then
  echo "ABORT: secret-like content found in staged files. Clean up and rerun." >&2
  rm -rf "$STAGE"
  exit 1
fi

# Zip it
cd "$STAGE"
if command -v zip >/dev/null 2>&1; then
  zip -rq "$OUT_ZIP" .
else
  powershell.exe -NoProfile -Command "Compress-Archive -Path '*' -DestinationPath '$OUT_ZIP' -Force"
fi

rm -rf "$STAGE"

SIZE=$(du -h "$OUT_ZIP" | cut -f1)
echo ""
echo "Done: $OUT_ZIP ($SIZE, $COUNT files)"
echo "Upload this to claude.ai -> Projects -> DMS Architecture -> Project knowledge."

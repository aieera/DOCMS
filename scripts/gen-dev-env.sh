#!/usr/bin/env bash
# Generate a local .env from .env.example with random dev secrets.
# Refuses to overwrite an existing .env (prevents accidental secret loss).
set -euo pipefail

cd "$(dirname "$0")/.."

if [ -f .env ]; then
  echo ".env already exists — delete it first if you want a fresh one."
  exit 0
fi

if [ ! -f .env.example ]; then
  echo ".env.example missing" >&2
  exit 1
fi

if ! command -v openssl >/dev/null 2>&1; then
  echo "openssl required for secret generation" >&2
  exit 1
fi

cp .env.example .env

# Fill in secrets. The sed in-place flag differs between BSD (macOS) and
# GNU — detect and call accordingly.
if sed --version >/dev/null 2>&1; then
  SED_I=(-i)
else
  SED_I=(-i '')
fi

KEK=$(openssl rand -base64 32)
INTERNAL=$(openssl rand -hex 32)
COOKIE=$(openssl rand -base64 32)

# Escape slashes + ampersands for sed replacement safety.
esc() { printf '%s' "$1" | sed 's/[\/&]/\\&/g'; }

sed "${SED_I[@]}" "s|^VAULTDMS_LOCAL_KEK=.*|VAULTDMS_LOCAL_KEK=$(esc "$KEK")|"       .env
sed "${SED_I[@]}" "s|^VAULTDMS_INTERNAL_API_KEY=.*|VAULTDMS_INTERNAL_API_KEY=$INTERNAL|" .env
sed "${SED_I[@]}" "s|^SESSION_COOKIE_SECRET=.*|SESSION_COOKIE_SECRET=$(esc "$COOKIE")|" .env

echo ".env generated with fresh dev secrets."
echo "  VAULTDMS_LOCAL_KEK, VAULTDMS_INTERNAL_API_KEY, SESSION_COOKIE_SECRET → randomized"
echo "  (never commit .env; it is in .gitignore)"

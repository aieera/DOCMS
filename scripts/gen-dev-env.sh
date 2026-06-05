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
# Hex (no slashes) so it's sed-safe and matches the compose hint
# (`openssl rand -hex 32`).
GATEWAY=$(openssl rand -hex 32)

# Escape slashes + ampersands for sed replacement safety.
esc() { printf '%s' "$1" | sed 's/[\/&]/\\&/g'; }

sed "${SED_I[@]}" "s|^SEDOC_LOCAL_KEK=.*|SEDOC_LOCAL_KEK=$(esc "$KEK")|"       .env
sed "${SED_I[@]}" "s|^SEDOC_INTERNAL_API_KEY=.*|SEDOC_INTERNAL_API_KEY=$INTERNAL|" .env
sed "${SED_I[@]}" "s|^SESSION_COOKIE_SECRET=.*|SESSION_COOKIE_SECRET=$(esc "$COOKIE")|" .env

# SEDOC_GATEWAY_SECRET is :?-required by docker-compose (the stack refuses to
# start without it) AND must MATCH between the backend (.env) and the frontend
# dev proxy (web/.env, which signs requests the backend then verifies).
# Generate once and write to both, so `make setup` works out of the box.
sed "${SED_I[@]}" "s|^SEDOC_GATEWAY_SECRET=.*|SEDOC_GATEWAY_SECRET=$GATEWAY|" .env
if [ -f web/.env.example ] && [ ! -f web/.env ]; then
  cp web/.env.example web/.env
  sed "${SED_I[@]}" "s|^SEDOC_GATEWAY_SECRET=.*|SEDOC_GATEWAY_SECRET=$GATEWAY|" web/.env
  WEB_NOTE=" + web/.env"
fi

echo ".env generated with fresh dev secrets."
echo "  SEDOC_LOCAL_KEK, SEDOC_INTERNAL_API_KEY, SEDOC_GATEWAY_SECRET, SESSION_COOKIE_SECRET → randomized"
echo "  SEDOC_GATEWAY_SECRET written to .env${WEB_NOTE:-} (backend + frontend must match)."
echo "  (never commit .env; it is in .gitignore)"

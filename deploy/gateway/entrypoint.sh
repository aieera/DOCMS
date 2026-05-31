#!/bin/sh
# Render the declarative Kong config at boot, substituting the
# gateway-signature shared secret from the environment. Kong's DBless
# auto-load of KONG_DECLARATIVE_CONFIG does NOT template env vars (that
# is a decK-time feature), so the `${VAULTDMS_GATEWAY_SECRET}` token in
# kong.yaml would otherwise be forwarded to backends verbatim and fail
# pkg/middleware.RequireGatewaySignature. We resolve it here, write the
# result to a writable path, and hand off to Kong's stock entrypoint.
set -eu

: "${VAULTDMS_GATEWAY_SECRET:?VAULTDMS_GATEWAY_SECRET must be set}"

SRC=/kong/declarative/kong.yaml
OUT=/tmp/kong.resolved.yaml

# The secret is 64 hex chars (openssl rand -hex 32) — no sed metacharacters,
# so a plain substitution is safe. `\$` keeps the search literal; the bare
# expansion injects the value.
sed "s|\${VAULTDMS_GATEWAY_SECRET}|${VAULTDMS_GATEWAY_SECRET}|g" "$SRC" > "$OUT"

export KONG_DECLARATIVE_CONFIG="$OUT"
exec /docker-entrypoint.sh kong docker-start

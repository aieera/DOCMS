#!/usr/bin/env bash
# Dev helper: start every Go service locally in the background, tailing
# logs to tmpfile per service. Each service gets its own GRPC_PORT and
# HEALTH_PORT since viper reads a single global VAULTDMS_GRPC_PORT /
# VAULTDMS_HEALTH_PORT — collisions otherwise.
set -euo pipefail

cd "$(dirname "$0")/.."

if [ ! -f .env ]; then
  echo ".env missing — run 'make gen-env' first" >&2
  exit 1
fi

# shellcheck disable=SC1091
set -a; source .env; set +a

# Preflight: every subscribe/publish subject must be covered by a stream
# before we spawn anything. Skip with SKIP_PREFLIGHT=1 (discouraged).
if [ "${SKIP_PREFLIGHT:-0}" != "1" ]; then
  # shellcheck disable=SC1091
  source "$(dirname "$0")/preflight.sh"
  if ! preflight; then
    echo "Refusing to start services with broken NATS topology. Set SKIP_PREFLIGHT=1 to bypass." >&2
    exit 1
  fi
fi

mkdir -p .run
PID_FILE=.run/services.pids
: > "$PID_FILE"

# service:grpc_port:health_port:http_port — viper reads global VAULTDMS_*_PORT
# env vars, so each service gets its own triple. HTTP ports (8180+) are
# distinct from health ports (8081+) so the two muxes don't collide.
# Frontend Vite proxy routes /api/* → these HTTP ports (see web/vite.config.ts).
service_specs=(
  "auth:9090:8081:8180"
  "policy:9091:8082:8181"
  "document:9092:8083:8182"
  "storage:9093:8084:8183"
  "search:9094:8085:8184"
  "audit:9095:8086:8185"
  "workflow:9096:8087:8186"
  "notification:9097:8088:8187"
  "signature:9098:8089:8188"
  "billing:9099:8090:8189"
  "connector:9100:8091:8190"
  "acknowledgement:9101:8092:8191"
)

# Inter-service addresses must match the per-service port assignments above.
export POLICY_SERVICE_ADDR="localhost:9091"
export STORAGE_SERVICE_ADDR="localhost:9093"

# §3.1 / B2.3 — gateway-signature shared secret; pkg/middleware
# RequireGatewaySignature refuses to start without it. Matches the
# default wired into docker-compose.yml so `make run-all` on the host
# interoperates with a compose gateway.
export VAULTDMS_GATEWAY_SECRET="${VAULTDMS_GATEWAY_SECRET:-dev-only-gateway-secret-rotate-in-prod}"

# ADR 0031 — /internal/* auth plane. Dev runs in HMAC-only mode with a
# shared secret; mTLS is enabled via cert-manager in k8s and (for local
# hacking) via mkcert below. VAULTDMS_TRUSTED_PROXY_CIDRS covers the
# loopback range so XFF headers sent by the compose gateway are
# honoured. In production both must be tightened — see the runbook at
# docs/runbooks/internal-mtls-bootstrap.md.
export VAULTDMS_INTERNAL_AUTH_MODE="${VAULTDMS_INTERNAL_AUTH_MODE:-hmac}"
export VAULTDMS_INTERNAL_HMAC_SECRET="${VAULTDMS_INTERNAL_HMAC_SECRET:-dev-only-internal-hmac-rotate-in-prod}"
export VAULTDMS_TRUSTED_PROXY_CIDRS="${VAULTDMS_TRUSTED_PROXY_CIDRS:-127.0.0.0/8,::1/128,10.0.0.0/8,172.16.0.0/12,192.168.0.0/16}"

# Optional: bootstrap mTLS material via mkcert when the operator asks
# for it (VAULTDMS_INTERNAL_AUTH_MODE=mtls or both). mkcert manages a
# local development CA it trusts; we reuse that as the internal CA for
# the duration of the dev session. Skipped silently when mkcert is not
# installed and hmac mode is enough.
if [ "$VAULTDMS_INTERNAL_AUTH_MODE" != "hmac" ]; then
  if ! command -v mkcert >/dev/null 2>&1; then
    echo "VAULTDMS_INTERNAL_AUTH_MODE=$VAULTDMS_INTERNAL_AUTH_MODE but mkcert is not on PATH." >&2
    echo "Install mkcert (https://github.com/FiloSottile/mkcert) or fall back to hmac mode." >&2
    exit 1
  fi
  mkdir -p .dev-certs
  if [ ! -f .dev-certs/ca.pem ]; then
    echo "Bootstrapping dev internal CA via mkcert → .dev-certs/"
    cp "$(mkcert -CAROOT)/rootCA.pem" .dev-certs/ca.pem
    # One leaf good for every in-cluster SAN we allowlist. The SAN
    # list here must match VAULTDMS_INTERNAL_SAN_ALLOWLIST below.
    (cd .dev-certs && mkcert \
      -cert-file client.pem -key-file client-key.pem \
      worker.temporal.internal sweeper.ack.internal \
      policy.internal signature.internal notification.internal \
      audit.internal auth.internal) >/dev/null
  fi
  export VAULTDMS_INTERNAL_CA_CERT="$(pwd)/.dev-certs/ca.pem"
  export VAULTDMS_INTERNAL_CLIENT_CERT="$(pwd)/.dev-certs/client.pem"
  export VAULTDMS_INTERNAL_CLIENT_KEY="$(pwd)/.dev-certs/client-key.pem"
  export VAULTDMS_INTERNAL_SAN_ALLOWLIST="worker.temporal.internal,sweeper.ack.internal,policy.internal,signature.internal,notification.internal,audit.internal,auth.internal"
fi

echo "Starting ${#service_specs[@]} services..."
for spec in "${service_specs[@]}"; do
  IFS=':' read -r svc grpc_port health_port http_port <<<"$spec"
  log=".run/$svc.log"
  (
    cd "services/$svc" \
      && VAULTDMS_GRPC_PORT="$grpc_port" \
         VAULTDMS_HEALTH_PORT="$health_port" \
         VAULTDMS_HTTP_PORT="$http_port" \
         go run ./cmd/server > "../../$log" 2>&1
  ) &
  echo "$!:$svc" >> "$PID_FILE"
  echo "  $svc → pid $! (grpc=$grpc_port health=$health_port http=$http_port log=$log)"
done

echo ""
echo "All services started. Stop with:"
echo "  cat .run/services.pids | cut -d: -f1 | xargs kill"
echo "or:"
echo "  make stop-all"

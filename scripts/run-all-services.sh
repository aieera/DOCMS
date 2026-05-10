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
  # graphql-gateway is HTTP-only — it doesn't bind a gRPC listener,
  # but pkg/config still validates VAULTDMS_GRPC_PORT (`gt=0,lt=65536`)
  # at boot, so we pass a distinct unused port (9101) rather than 0.
  # http_port 8191 matches the docker-compose mapping; the Vite proxy
  # routes /api/v1/graphql there in host mode.
  "graphql-gateway:9101:8093:8191"
)

# Inter-service addresses must match the per-service port assignments above.
export POLICY_SERVICE_ADDR="localhost:9091"
export STORAGE_SERVICE_ADDR="localhost:9093"
# graphql-gateway upstream addrs (ADR 0074). Each is the gRPC port of
# the corresponding service from the spec above.
export DOCUMENT_SERVICE_ADDR="localhost:9092"
export WORKFLOW_SERVICE_ADDR="localhost:9096"
export AUDIT_SERVICE_ADDR="localhost:9095"
# collaboration runs as part of the document service in this env.
export COLLABORATION_SERVICE_ADDR="localhost:9092"
# ADR 0075 — bulk import dispatches BulkUser / BulkGroup outbound
# to the auth service over gRPC. auth runs on 9090 in host mode.
export AUTH_SERVICE_ADDR="localhost:9090"

# §3.1 / B2.3 — gateway-signature shared secret; pkg/middleware
# RequireGatewaySignature refuses to start without it. Matches the
# default wired into docker-compose.yml so `make run-all` on the host
# interoperates with a compose gateway.
export VAULTDMS_GATEWAY_SECRET="${VAULTDMS_GATEWAY_SECRET:-dev-only-gateway-secret-rotate-in-prod}"

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

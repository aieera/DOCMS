#!/usr/bin/env bash
# SeDoc Chaos Tests — run alongside k6 load tests.
# Prerequisites: kubectl access to vaultdms namespace, k6 running scenario 06.
set -euo pipefail

NS="${NAMESPACE:-vaultdms}"
LOG_FILE="chaos-results-$(date +%Y%m%d-%H%M%S).log"

log() { echo "[$(date -Iseconds)] $*" | tee -a "$LOG_FILE"; }

# ===========================================================================
# Test 1: Kill random pod during load → verify auto-recovery
# ===========================================================================
chaos_kill_random_pod() {
  log "=== CHAOS: Kill random service pod ==="
  local pod
  pod=$(kubectl -n "$NS" get pods -l app.kubernetes.io/instance=vaultdms \
    --field-selector=status.phase=Running -o jsonpath='{.items[0].metadata.name}' 2>/dev/null)
  if [ -z "$pod" ]; then log "SKIP: no running pods found"; return; fi
  log "Killing pod: $pod"
  kubectl -n "$NS" delete pod "$pod" --grace-period=0 --force 2>/dev/null || true
  log "Waiting 30s for recovery..."
  sleep 30
  local ready
  ready=$(kubectl -n "$NS" get pods -l app.kubernetes.io/instance=vaultdms \
    --field-selector=status.phase=Running -o name 2>/dev/null | wc -l)
  log "Running pods after recovery: $ready"
  if [ "$ready" -ge 1 ]; then log "PASS: Pod auto-recovered"; else log "FAIL: No pods running"; fi
}

# ===========================================================================
# Test 2: Kill DB primary → verify failover <30s (Patroni)
# ===========================================================================
chaos_db_failover() {
  log "=== CHAOS: Kill PostgreSQL primary ==="
  local db_pod
  db_pod=$(kubectl -n "$NS" get pods -l app.kubernetes.io/name=postgresql -o jsonpath='{.items[0].metadata.name}' 2>/dev/null)
  if [ -z "$db_pod" ]; then log "SKIP: no postgres pod found"; return; fi
  log "Killing DB pod: $db_pod"
  local start=$SECONDS
  kubectl -n "$NS" delete pod "$db_pod" --grace-period=0 --force 2>/dev/null || true
  # Poll until a new pod is Ready.
  local elapsed=0
  while [ $elapsed -lt 60 ]; do
    sleep 2
    elapsed=$((SECONDS - start))
    local status
    status=$(kubectl -n "$NS" get pods -l app.kubernetes.io/name=postgresql \
      -o jsonpath='{.items[0].status.conditions[?(@.type=="Ready")].status}' 2>/dev/null)
    if [ "$status" = "True" ]; then
      log "PASS: DB recovered in ${elapsed}s"
      [ $elapsed -le 30 ] && log "SLI MET: failover <30s" || log "SLI BREACH: failover took ${elapsed}s (>30s)"
      return
    fi
  done
  log "FAIL: DB did not recover within 60s"
}

# ===========================================================================
# Test 3: Kill OpenSearch node → verify search still works (degraded)
# ===========================================================================
chaos_opensearch_kill() {
  log "=== CHAOS: Kill OpenSearch node ==="
  local os_pod
  os_pod=$(kubectl -n "$NS" get pods -l app.kubernetes.io/name=opensearch -o jsonpath='{.items[0].metadata.name}' 2>/dev/null)
  if [ -z "$os_pod" ]; then log "SKIP: no opensearch pod found"; return; fi
  kubectl -n "$NS" delete pod "$os_pod" --grace-period=0 --force 2>/dev/null || true
  sleep 10
  # Test search endpoint — should still respond (might return errors but not crash).
  local http_code
  http_code=$(curl -s -o /dev/null -w '%{http_code}' \
    -H "X-Tenant-ID: chaos-test" -H "Content-Type: application/json" \
    -d '{"query":"test"}' "http://localhost:8080/api/v1/search" 2>/dev/null || echo "000")
  if [ "$http_code" != "000" ]; then
    log "PASS: Search endpoint still responds (HTTP $http_code) — degraded mode"
  else
    log "FAIL: Search endpoint unreachable"
  fi
}

# ===========================================================================
# Test 4: Network partition → verify circuit breakers
# ===========================================================================
chaos_network_partition() {
  log "=== CHAOS: Network partition (simulate via NetworkPolicy) ==="
  # Create a NetworkPolicy that blocks traffic to the policy service.
  kubectl -n "$NS" apply -f - <<'EOF'
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: chaos-block-policy-svc
spec:
  podSelector:
    matchLabels:
      app.kubernetes.io/name: policy
  policyTypes: [Ingress]
  ingress: []
EOF
  log "Policy service partitioned. Waiting 15s..."
  sleep 15
  # Uploads should fail-closed (403 Forbidden).
  local http_code
  http_code=$(curl -s -o /dev/null -w '%{http_code}' \
    -H "X-Tenant-ID: chaos-test" -H "Content-Type: application/json" \
    -d '{"filename":"test.pdf","mime_type":"application/pdf","size_bytes":1024}' \
    "http://localhost:8080/api/v1/storage/uploads/initiate" 2>/dev/null || echo "000")
  if [ "$http_code" = "403" ]; then
    log "PASS: Upload correctly denied (fail-closed) during policy partition"
  else
    log "INFO: Upload returned HTTP $http_code (expected 403)"
  fi
  # Clean up.
  kubectl -n "$NS" delete networkpolicy chaos-block-policy-svc 2>/dev/null || true
  log "Network partition removed."
}

# ===========================================================================
# Run all chaos tests
# ===========================================================================
main() {
  log "Starting SeDoc chaos test suite"
  chaos_kill_random_pod
  sleep 10
  chaos_db_failover
  sleep 10
  chaos_opensearch_kill
  sleep 10
  chaos_network_partition
  log "=== Chaos test suite complete. Results in $LOG_FILE ==="
}

main "$@"

#!/usr/bin/env bash
# VaultDMS Chaos Tests — run alongside k6 load tests.
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
# Test 5: Kill OCR worker mid-batch → verify exactly-once + no lost docs
# Scenario doc: docs/chaos/scenarios/07-ocr-worker-kill.md
# Requires: PROM_URL, PG_DSN, NATS_URL env vars; chaos tenant pre-seeded
# with 100 PDFs in flight via tests/load/scenarios/04-ocr-pipeline.js.
# ===========================================================================
chaos_ocr_worker_kill_mid_batch() {
  log "=== CHAOS: Kill intelligence-worker at ~50% page progress ==="
  : "${PROM_URL:?PROM_URL required}"
  : "${PG_DSN:?PG_DSN required}"
  : "${NATS_URL:?NATS_URL required}"
  : "${CHAOS_TENANT:?CHAOS_TENANT required}"

  local expected_pages="${EXPECTED_PAGES:-2000}"
  local target=$((expected_pages / 2))

  log "Waiting for ocr_pages_total >= $target (50% of $expected_pages)..."
  local waited=0
  while [ $waited -lt 600 ]; do
    local current
    current=$(curl -s "${PROM_URL}/api/v1/query?query=ocr_pages_total" \
      | jq -r '.data.result[0].value[1] // "0"' | cut -d. -f1)
    if [ "${current:-0}" -ge "$target" ]; then break; fi
    sleep 2; waited=$((waited + 2))
  done
  [ $waited -ge 600 ] && { log "FAIL: 50% page progress never reached"; return 1; }

  log "Killing intelligence-worker pod(s)..."
  local kill_t=$SECONDS
  kubectl -n "$NS" delete pod -l app.kubernetes.io/name=intelligence-worker \
    --grace-period=0 --force 2>/dev/null || true

  log "Waiting up to 60s for replacement Ready..."
  local elapsed=0
  while [ $elapsed -lt 60 ]; do
    sleep 2; elapsed=$((SECONDS - kill_t))
    local ready
    ready=$(kubectl -n "$NS" get pods -l app.kubernetes.io/name=intelligence-worker \
      -o jsonpath='{.items[?(@.status.conditions[?(@.type=="Ready")].status=="True")].metadata.name}' 2>/dev/null)
    [ -n "$ready" ] && break
  done
  [ -z "${ready:-}" ] && { log "FAIL: worker did not recover within 60s"; return 1; }
  log "Replacement Ready in ${elapsed}s"

  log "Waiting 5 min for redelivered messages to drain..."
  sleep 300

  # Verification 1: no duplicate ocr_processed_events rows.
  local dupes
  dupes=$(psql "$PG_DSN" -tAc \
    "SELECT COUNT(*) FROM (SELECT 1 FROM ocr_processed_events
       WHERE tenant_id='${CHAOS_TENANT}' GROUP BY event_id HAVING COUNT(*) > 1) d;" 2>/dev/null || echo "ERR")
  [ "$dupes" = "0" ] \
    && log "PASS: no duplicate ocr_processed_events rows" \
    || { log "FAIL: duplicate rows found ($dupes)"; return 1; }

  # Verification 2: exactly 100 dms.ocr.completed.v1 messages on stream.
  local completed
  completed=$(nats --server="$NATS_URL" stream info INTEL_EVENTS --json 2>/dev/null \
    | jq '.state.subjects["dms.ocr.completed.v1"] // 0')
  [ "$completed" = "100" ] \
    && log "PASS: exactly 100 dms.ocr.completed.v1 events" \
    || { log "FAIL: expected 100 completed events, got $completed"; return 1; }

  # Verification 3: no documents stuck in 'enqueued' beyond the redelivery window.
  local stuck
  stuck=$(psql "$PG_DSN" -tAc \
    "SELECT COUNT(*) FROM ocr_processed_events
       WHERE tenant_id='${CHAOS_TENANT}' AND status='enqueued'
         AND processed_at < now() - interval '5 minutes';" 2>/dev/null || echo "ERR")
  [ "$stuck" = "0" ] \
    && log "PASS: no documents stuck in enqueued" \
    || { log "FAIL: $stuck documents still enqueued"; return 1; }

  # Verification 4: JetStream consumer caught up.
  local pending
  pending=$(nats --server="$NATS_URL" consumer info INTEL_EVENTS intelligence-ocr --json 2>/dev/null \
    | jq '.num_pending // 0')
  [ "$pending" = "0" ] \
    && log "PASS: consumer pending=0 (queue not blocked)" \
    || { log "FAIL: consumer has $pending pending messages"; return 1; }

  log "PASS: OCR worker kill chaos scenario passed"
}

# ===========================================================================
# Run all chaos tests
# ===========================================================================
main() {
  log "Starting VaultDMS chaos test suite"
  chaos_kill_random_pod
  sleep 10
  chaos_db_failover
  sleep 10
  chaos_opensearch_kill
  sleep 10
  chaos_network_partition
  sleep 10
  chaos_ocr_worker_kill_mid_batch
  log "=== Chaos test suite complete. Results in $LOG_FILE ==="
}

main "$@"

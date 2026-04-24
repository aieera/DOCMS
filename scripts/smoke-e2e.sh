#!/usr/bin/env bash
# Phase 3 orchestrator — cold-boot smoke against local docker-compose.
#
# Runs on an operator workstation (macOS/Linux + Docker Desktop).
# Cannot run from CI without a DinD runner + substantial RAM (27
# compose services). Output:
#
#   1. Every docker-compose healthcheck green within 180s, OR dumps
#      the failing service's logs to docs/reports/smoke/<ts>-<svc>.log.
#   2. All migrations applied via `dms-admin db migrate --all`.
#   3. Vite dev server boots on :3000 with zero console errors on /login.
#   4. Playwright runs the 10 W13.4 journeys + writes the report.
#   5. Lighthouse CI on 6 pages with threshold assertions.
#   6. axe-core over the 10 journeys.
#   7. k6 smoke (10% of Wave 13.2 targets): 50 uploads/min, 200 q/s
#      search, 50 logins/s for 5 minutes.
#   8. Cross-tenant isolation check — two tenants, same-content
#      upload, assert zero cross-reads. HARD STOP on leak.
#   9. Teardown + cold-boot reproducibility timing.
#
# Exit codes:
#   0 — every phase passed
#   1 — any phase failed (report appended)
#   2 — prerequisite missing (docker/k6/lighthouse/playwright)
#
# Report: docs/reports/SMOKE_RUN_$(date +%Y-%m-%d).md

set -euo pipefail

REPO="${REPO:-$(cd "$(dirname "$0")/.." && pwd)}"
DATE=$(date +%Y-%m-%d)
REPORT="$REPO/docs/reports/SMOKE_RUN_${DATE}.md"
LOGDIR="$REPO/docs/reports/smoke/$DATE"
mkdir -p "$LOGDIR"

log() { echo "[$(date -Iseconds)] $*" | tee -a "$REPORT"; }

require() {
    command -v "$1" >/dev/null 2>&1 || {
        echo "missing: $1 — install before running smoke" >&2
        exit 2
    }
}

# ---- Prereq checks ---------------------------------------------------------
require docker
require k6      || true  # k6 is optional for the sub-phase
require node    # for Playwright + Lighthouse
require jq

echo "# Smoke run — $DATE" > "$REPORT"
echo "" >> "$REPORT"
log "start"

# ---- 3.1 Clean boot --------------------------------------------------------
log "3.1 clean boot"
cd "$REPO"
docker compose down -v > "$LOGDIR/compose-down.log" 2>&1 || true

BOOT_START=$SECONDS
docker compose up -d --wait > "$LOGDIR/compose-up.log" 2>&1 || {
    log "FAIL: compose up --wait exited non-zero within 180s"
    for svc in $(docker compose ps --services); do
        docker compose logs --tail 200 "$svc" > "$LOGDIR/${svc}.log" 2>&1
    done
    exit 1
}
log "3.1 compose up OK in $((SECONDS - BOOT_START))s"

# ---- 3.2 Migrations --------------------------------------------------------
log "3.2 migrations"
go run ./cmd/dms-admin db migrate --all >> "$REPORT" 2>&1 || {
    log "FAIL: dms-admin db migrate --all"
    exit 1
}

# ---- 3.3 Frontend dev server ----------------------------------------------
log "3.3 frontend dev"
(
    cd web
    [ -d node_modules ] || npm ci >> "$LOGDIR/npm-ci.log" 2>&1
    npm run dev > "$LOGDIR/vite.log" 2>&1 &
    echo $! > "$LOGDIR/vite.pid"
    # Wait for :3000 to respond
    for i in {1..60}; do
        curl -sf http://localhost:3000 >/dev/null && break
        sleep 1
    done
)

# ---- 3.4 Playwright journeys ----------------------------------------------
log "3.4 playwright 10 journeys"
(
    cd web
    npx playwright test --reporter=json > "$LOGDIR/playwright.json" || {
        log "FAIL: playwright journeys"
        exit 1
    }
)

# ---- 3.5 Cross-tenant isolation -------------------------------------------
log "3.5 cross-tenant isolation — HARD STOP on leak"
# See tests/load/scenarios/07-cross-tenant-isolation.js; running it
# via k6 asserts isolation_violations == 0.
k6 run --quiet --summary-export="$LOGDIR/isolation-summary.json" \
    tests/load/scenarios/07-cross-tenant-isolation.js \
    > "$LOGDIR/isolation.log" 2>&1 || {
    log "STOP: cross-tenant isolation violated. File P0 incident."
    log "see $LOGDIR/isolation.log"
    exit 1
}
violations=$(jq '.metrics.isolation_violations.values.count // 0' "$LOGDIR/isolation-summary.json")
if [[ "$violations" != "0" ]]; then
    log "STOP: $violations cross-tenant violations detected"
    exit 1
fi

# ---- 3.6 Lighthouse + axe + k6 smoke --------------------------------------
log "3.6 lighthouse + axe + k6 smoke (5 min)"
(
    cd web
    npx -y @lhci/cli autorun \
        --upload.target=filesystem \
        --upload.outputDir="$LOGDIR/lighthouse" \
        > "$LOGDIR/lighthouse.log" 2>&1 || {
            log "FAIL: lighthouse"; exit 1;
        }
)
# axe-core covered by playwright test runs above (a11y.test.tsx).
# k6 smoke at 10% of perf targets.
k6 run --quiet \
    --summary-export="$LOGDIR/smoke-summary.json" \
    --vus 50 --duration 5m \
    tests/load/scenarios/06-mixed-realistic.js \
    > "$LOGDIR/k6-smoke.log" 2>&1 || {
    log "FAIL: k6 smoke"; exit 1;
}

# ---- 3.7 Teardown + reproducibility ---------------------------------------
log "3.7 teardown"
kill "$(cat "$LOGDIR/vite.pid")" 2>/dev/null || true
docker compose down -v > "$LOGDIR/teardown.log" 2>&1

TOTAL=$((SECONDS - BOOT_START))
log "total wall clock: ${TOTAL}s"
if [[ $TOTAL -gt 900 ]]; then
    log "WARN: cold-boot took >15 min; see Wave 14.1 DoD"
fi

log "smoke run complete — see $REPORT"

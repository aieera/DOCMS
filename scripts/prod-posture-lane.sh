#!/usr/bin/env bash
# prod-posture-lane.sh — run the prod-posture (NOBYPASSRLS) integration
# lane and reconcile results against the known-failing allow-list.
#
# WHY: prod Postgres enforces NOBYPASSRLS on the app role (helm
# postgres-cluster.yaml postInit; pkg/database/rls_posture.go), so any
# query without app.current_tenant fails closed. Dev masks this with
# SEDOC_ALLOW_BYPASS_RLS=1, which is how the audit's RLS tenant-context
# gaps stayed invisible (docs/STATE_OF_THE_PROJECT.md 2026-07-03). This
# lane runs the TestProdPosture_* integration tests — each connects as a
# dms_app NOBYPASSRLS role via pkg/testutil.NewProdPostureDB — and stays
# "green with known failures" via ci/prod-posture-allowlist.txt.
#
# The lane FAILS when:
#   1. the bypass env var is set (the lane would be meaningless),
#   2. any package fails to build,
#   3. a test NOT in the allow-list fails (regression),
#   4. an allow-listed test PASSES (stale entry — the Wave A fix landed;
#      remove the line in the same PR),
#   5. an allow-listed test didn't run (repro renamed/deleted).
#
# Scope note: -run '^TestProdPosture_' keeps the lane to posture-specific
# tests. Posture is per-test wiring (testcontainers own the DB), so
# rerunning the superuser-posture integration suite here adds nothing;
# widening the scope is blocked anyway until migration 000021 (STATE
# "Migration blocker") stops breaking document's suite on a clean DB.
set -u -o pipefail

cd "$(dirname "$0")/.."

# Overridable for testing the reconciliation logic itself.
ALLOWLIST="${PROD_POSTURE_ALLOWLIST:-ci/prod-posture-allowlist.txt}"
RUN_PATTERN='^TestProdPosture_'
OUT="$(mktemp)"
trap 'rm -f "$OUT"' EXIT

if [ "${SEDOC_ALLOW_BYPASS_RLS:-}" = "1" ]; then
  echo "FATAL: SEDOC_ALLOW_BYPASS_RLS=1 is set — the prod-posture lane must run without the dev bypass opt-in." >&2
  exit 1
fi

# Path patterns like ./services/... do NOT traverse go.work modules from
# the repo root (go answers "directory prefix services does not contain
# modules listed in go.work"), so iterate the workspace modules
# explicitly. Repro tests live in pkg/ and services/* by convention.
mapfile -t MODULE_DIRS < <(go list -m -f '{{.Dir}}' | grep -E '/(pkg|services/[a-z0-9-]+)$')
if [ "${#MODULE_DIRS[@]}" -eq 0 ]; then
  echo "FATAL: no workspace modules found — go.work missing or go list broken." >&2
  exit 1
fi

: > "$OUT"
for dir in "${MODULE_DIRS[@]}"; do
  echo ">> prod-posture lane: (cd ${dir#"$PWD"/} && go test -tags \"integration prodposture\" -run '${RUN_PATTERN}' ./...)"
  # -v is load-bearing: without it go test prints no "--- PASS:" lines,
  # and the stale-entry check (allow-listed test now passing) can't see
  # passes at all.
  (cd "$dir" && go test -v -tags "integration prodposture" -run "${RUN_PATTERN}" -timeout 20m ./... 2>&1) | tee -a "$OUT" || true
done

if grep -q '\[build failed\]\|\[setup failed\]' "$OUT"; then
  echo "FATAL: build/setup failure in the lane (see above) — allow-listing covers test failures only." >&2
  exit 1
fi

# Top-level results only: subtest lines are indented, so anchor at col 0.
mapfile -t FAILED < <(grep '^--- FAIL: ' "$OUT" | awk '{print $3}' | sort -u)
mapfile -t PASSED < <(grep '^--- PASS: ' "$OUT" | awk '{print $3}' | sort -u)
mapfile -t ALLOWED < <(grep -Ev '^\s*(#|$)' "$ALLOWLIST" | awk '{print $1}' | sort -u)

contains() { local x needle="$1"; shift; for x in "$@"; do [ "$x" = "$needle" ] && return 0; done; return 1; }

rc=0

for t in "${FAILED[@]:-}"; do
  [ -n "$t" ] || continue
  if ! contains "$t" "${ALLOWED[@]:-}"; then
    echo "FAIL: $t failed and is NOT in $ALLOWLIST — this is a prod-posture regression." >&2
    rc=1
  fi
done

for t in "${ALLOWED[@]:-}"; do
  [ -n "$t" ] || continue
  if contains "$t" "${PASSED[@]:-}"; then
    echo "FAIL: allow-listed $t PASSED — its Wave A fix landed; remove its line from $ALLOWLIST in this PR." >&2
    rc=1
  elif ! contains "$t" "${FAILED[@]:-}"; then
    echo "FAIL: allow-listed $t did not run — repro test renamed or deleted? The allow-list must track real tests." >&2
    rc=1
  fi
done

known=0
for t in "${FAILED[@]:-}"; do
  [ -n "$t" ] || continue
  contains "$t" "${ALLOWED[@]:-}" && known=$((known + 1))
done

if [ "$rc" -eq 0 ]; then
  echo ">> prod-posture lane: GREEN with ${known} known failure(s) (allow-listed Wave A gaps — see ci/prod-posture-allowlist.txt)."
else
  echo ">> prod-posture lane: RED — see failures above." >&2
fi
exit "$rc"

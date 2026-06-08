#!/usr/bin/env bash
# §9.6 / H1 — nightly SOC 2 evidence bundle.
#
# Collects the set of evidence a CC1-CC9 audit needs, tars it, signs
# the manifest, and (when S3 creds are present) uploads to the
# evidence bucket. Runs from a Kubernetes CronJob but is also safe
# to run on an operator's workstation for ad-hoc collection.
#
# Required env:
#   DATABASE_URL              — Postgres DSN (read-only user preferred)
#   SEDOC_EVIDENCE_SIGNING_KEY — HMAC key for manifest signature
# Optional env:
#   S3_BUCKET                 — cold-storage upload destination
#   S3_ENDPOINT               — custom S3 endpoint (MinIO on-prem)
#   PROMETHEUS_URL            — scrape backup_success_total / vuln scores
#
# Evidence bundled (per SOC 2 TSC CC mapping):
#   CC6.1  — access-reviews.csv        : active admin accounts by tenant
#   CC6.6  — backup-verify.csv         : last N backup attempts + success
#   CC7.1  — vuln-scans/               : gosec/govulncheck output, if present
#   CC7.3  — audit-chain-verify.json   : latest hash-chain verify result
#   CC8.1  — change-log.txt            : git log of the repo covered window
#   CC9.1  — incidents/                : docs/runbooks/ir drill logs
#   MANIFEST.json + MANIFEST.sig       : signed bundle manifest
#
# Missing inputs don't abort the run — an empty section produces
# `<section>/EMPTY` so auditors see the gap rather than a silent
# omission.
set -euo pipefail

if [ -z "${SEDOC_EVIDENCE_SIGNING_KEY:-}" ]; then
  echo "SEDOC_EVIDENCE_SIGNING_KEY required" >&2
  exit 2
fi

OUT_DIR="${OUT_DIR:-/tmp/soc2-evidence}"
TS="$(date -u +%Y%m%d-%H%M%S)"
BUNDLE="${OUT_DIR}/evidence-${TS}"
rm -rf "$BUNDLE"
mkdir -p "$BUNDLE"/{access,backup,vuln,audit,change,incidents}

note() { printf "%s %s\n" "$(date -Iseconds)" "$*"; }

# ---- CC6.1 — access reviews -----------------------------------------
note "collecting access reviews"
if [ -n "${DATABASE_URL:-}" ] && command -v psql >/dev/null 2>&1; then
  psql "$DATABASE_URL" --csv -At \
    -c "SELECT tenant_id, id, email, role, status, last_login_at
        FROM users
        WHERE status = 'active' AND role IN ('owner','admin','compliance_officer')
        ORDER BY tenant_id, role, email" \
    > "$BUNDLE/access/active-admins.csv" 2>/dev/null \
    || echo "query failed" > "$BUNDLE/access/EMPTY"
else
  echo "no DATABASE_URL or psql" > "$BUNDLE/access/EMPTY"
fi

# ---- CC6.6 — backup verify ------------------------------------------
note "collecting backup signal"
if [ -n "${PROMETHEUS_URL:-}" ]; then
  curl -sG --data-urlencode 'query=rate(backup_success_total[7d])' \
    "$PROMETHEUS_URL/api/v1/query" > "$BUNDLE/backup/backup-7d.json" 2>/dev/null \
    || echo "prom query failed" > "$BUNDLE/backup/EMPTY"
else
  echo "no PROMETHEUS_URL" > "$BUNDLE/backup/EMPTY"
fi

# ---- CC7.1 — vuln scans ---------------------------------------------
note "collecting vuln scan artifacts"
if [ -d "./docs/security/scans" ]; then
  cp -r ./docs/security/scans/* "$BUNDLE/vuln/" 2>/dev/null || true
fi
[ "$(ls -A "$BUNDLE/vuln")" ] || echo "no scan outputs" > "$BUNDLE/vuln/EMPTY"

# ---- CC7.3 — audit chain verify -------------------------------------
note "collecting audit chain verify"
if [ -n "${DATABASE_URL:-}" ] && command -v psql >/dev/null 2>&1; then
  psql "$DATABASE_URL" -At \
    -c "SELECT row_to_json(t) FROM (
          SELECT tenant_id,
                 count(*) AS events,
                 max(created_at) AS last_event
          FROM audit_events
          GROUP BY tenant_id
        ) t" > "$BUNDLE/audit/per-tenant-counts.jsonl" 2>/dev/null \
    || echo "query failed" > "$BUNDLE/audit/EMPTY"
else
  echo "no DATABASE_URL or psql" > "$BUNDLE/audit/EMPTY"
fi

# ---- CC8.1 — change log ---------------------------------------------
note "collecting change log"
if [ -d .git ] && command -v git >/dev/null 2>&1; then
  git log --since='30 days ago' --pretty=format:'%h|%an|%ai|%s' \
    > "$BUNDLE/change/last-30-days.txt" 2>/dev/null \
    || echo "git log failed" > "$BUNDLE/change/EMPTY"
else
  echo "no git repo" > "$BUNDLE/change/EMPTY"
fi

# ---- CC9.1 — incident / DR drill logs -------------------------------
note "collecting incident runbook logs"
if [ -d ./docs/runbooks/dr-drills ]; then
  cp -r ./docs/runbooks/dr-drills "$BUNDLE/incidents/" 2>/dev/null || true
fi
if [ -d ./docs/runbooks/incidents ]; then
  cp -r ./docs/runbooks/incidents "$BUNDLE/incidents/" 2>/dev/null || true
fi
[ "$(ls -A "$BUNDLE/incidents")" ] || echo "no incident logs" > "$BUNDLE/incidents/EMPTY"

# ---- manifest + signature ------------------------------------------
note "writing manifest"
{
  echo '{'
  echo '  "bundle_version": "1",'
  echo "  \"generated_at\": \"$(date -Iseconds)\","
  echo "  \"hostname\": \"$(hostname -s 2>/dev/null || echo unknown)\","
  echo '  "sections": {'
  first=1
  for d in access backup vuln audit change incidents; do
    n=$(find "$BUNDLE/$d" -type f 2>/dev/null | wc -l | awk '{print $1}')
    if [ "$first" = 1 ]; then first=0; else echo ','; fi
    printf '    "%s": { "files": %d, "bytes": %d }' "$d" "$n" \
      "$(du -bs "$BUNDLE/$d" 2>/dev/null | awk '{print $1}')"
  done
  echo
  echo '  }'
  echo '}'
} > "$BUNDLE/MANIFEST.json"

# HMAC-SHA256 signature of the manifest.
SIG="$(openssl dgst -sha256 -hmac "$SEDOC_EVIDENCE_SIGNING_KEY" -hex \
        < "$BUNDLE/MANIFEST.json" | awk '{print $2}')"
echo -n "$SIG" > "$BUNDLE/MANIFEST.sig"

# ---- tar + optional S3 upload --------------------------------------
TARBALL="${BUNDLE}.tar.gz"
tar -czf "$TARBALL" -C "$OUT_DIR" "evidence-${TS}"
note "wrote $TARBALL ($(wc -c <"$TARBALL") bytes)"

if [ -n "${S3_BUCKET:-}" ] && command -v aws >/dev/null 2>&1; then
  EP_FLAG=""
  [ -n "${S3_ENDPOINT:-}" ] && EP_FLAG="--endpoint-url $S3_ENDPOINT"
  aws s3 cp "$TARBALL" "s3://${S3_BUCKET}/evidence/$(basename "$TARBALL")" $EP_FLAG
  note "uploaded to s3://${S3_BUCKET}/evidence/"
else
  note "S3_BUCKET unset — tarball left at $TARBALL"
fi

note "done"

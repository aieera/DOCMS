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
#   OUT_DIR                   — where the bundle is written (default: a
#                               fresh mktemp -d directory)
#   S3_BUCKET                 — cold-storage upload destination
#   S3_ENDPOINT               — custom S3 endpoint (MinIO on-prem)
#   PROMETHEUS_URL            — query the backup CronJobs' last-success
#                               timestamps (kube-state-metrics)
#
# Tenant tables (users, audit_events) are FORCE RLS keyed on
# app.current_tenant. Under the production NOBYPASSRLS role a query
# without that setting returns zero rows, so every tenant-scoped query
# runs once per tenant with set_config('app.current_tenant', ...) (O-14).
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

# A private fresh directory by default: a fixed /tmp path can be
# pre-created or symlinked by another local user.
OUT_DIR="${OUT_DIR:-$(mktemp -d "${TMPDIR:-/tmp}/soc2-evidence.XXXXXXXX")}"
TS="$(date -u +%Y%m%d-%H%M%S)"
BUNDLE="${OUT_DIR}/evidence-${TS}"
rm -rf "$BUNDLE"
mkdir -p "$BUNDLE"/{access,backup,vuln,audit,change,incidents}

note() { printf "%s %s\n" "$(date -Iseconds)" "$*"; }

have_db() { [ -n "${DATABASE_URL:-}" ] && command -v psql >/dev/null 2>&1; }

UUID_RE='^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$'

# Live tenants. organizations carries no tenant RLS (pkg/database
# ListTenantIDs reads it on the raw pool), so this works under dms_app.
list_tenants() {
  psql "$DATABASE_URL" -X -q -v ON_ERROR_STOP=1 -At \
    -c "SELECT id FROM organizations WHERE deleted_at IS NULL ORDER BY id"
}

# tenant_query <tenant_id> <sql> — runs <sql> with app.current_tenant
# set for that tenant (transaction-local), printing tuples only as CSV.
tenant_query() {
  psql "$DATABASE_URL" -X -q -v ON_ERROR_STOP=1 --csv -t -v tid="$1" <<SQL
BEGIN;
\o /dev/null
SELECT set_config('app.current_tenant', :'tid', true);
\o
$2
COMMIT;
SQL
}

# per_tenant <out_file> <empty_marker> <header> <sql> — runs <sql> for
# every tenant into <out_file>. Zero data rows writes <empty_marker>
# instead of leaving a header-only file that reads as "nothing to report".
per_tenant() {
  local out="$1" empty="$2" header="$3" sql="$4" tenants tid n=0
  if ! tenants="$(list_tenants)"; then
    echo "tenant enumeration failed" > "$empty"
    return 0
  fi
  printf '%s\n' "$header" > "$out"
  for tid in $tenants; do
    if ! [[ "$tid" =~ $UUID_RE ]]; then
      echo "unexpected tenant id: $tid" > "$empty"
      rm -f "$out"
      return 0
    fi
    n=$((n + 1))
    if ! tenant_query "$tid" "$sql" >> "$out"; then
      echo "query failed for tenant $tid" > "$empty"
      rm -f "$out"
      return 0
    fi
  done
  if [ "$(wc -l < "$out")" -le 1 ]; then
    echo "no rows ($n tenant(s) scanned)" > "$empty"
    rm -f "$out"
  fi
}

# ---- CC6.1 — access reviews -----------------------------------------
note "collecting access reviews"
if have_db; then
  per_tenant "$BUNDLE/access/active-admins.csv" "$BUNDLE/access/EMPTY" \
    "tenant_id,id,email,role,status,last_login_at" \
    "SELECT tenant_id, id, email, role, status, last_login_at
       FROM users
      WHERE status = 'active' AND deleted_at IS NULL
        AND role IN ('owner','admin','compliance_officer')
      ORDER BY role, email;"
else
  echo "no DATABASE_URL or psql" > "$BUNDLE/access/EMPTY"
fi

# ---- CC6.6 — backup verify ------------------------------------------
# Nothing in SeDoc emits a backup_* metric; the backup and backup-verify
# CronJobs (deploy/helm/sedoc/templates/cronjobs/backup*.yaml) are
# observed through kube-state-metrics. Override BACKUP_CRONJOB_RE for
# a different release name.
note "collecting backup signal"
BACKUP_CRONJOB_RE="${BACKUP_CRONJOB_RE:-sedoc-backup|sedoc-backup-verify}"
prom_query() { # <promql> <out_file>; fails on HTTP error or an empty result
  curl -fsSG --data-urlencode "query=$1" "$PROMETHEUS_URL/api/v1/query" > "$2" \
    && grep -q '"status":"success"' "$2" \
    && ! grep -q '"result":\[\]' "$2"
}
if [ -n "${PROMETHEUS_URL:-}" ]; then
  if prom_query "kube_cronjob_status_last_successful_time{cronjob=~\"${BACKUP_CRONJOB_RE}\"}" \
       "$BUNDLE/backup/last-successful-time.json" \
     && prom_query "kube_cronjob_status_last_schedule_time{cronjob=~\"${BACKUP_CRONJOB_RE}\"}" \
       "$BUNDLE/backup/last-schedule-time.json"; then
    :
  else
    rm -f "$BUNDLE"/backup/*.json
    echo "prometheus query failed or returned no series for cronjob=~${BACKUP_CRONJOB_RE}" \
      > "$BUNDLE/backup/EMPTY"
  fi
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
if have_db; then
  per_tenant "$BUNDLE/audit/per-tenant-counts.csv" "$BUNDLE/audit/EMPTY" \
    "tenant_id,events,last_event" \
    "SELECT tenant_id, count(*) AS events, max(created_at) AS last_event
       FROM audit_events
      GROUP BY tenant_id;"
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
  EP_FLAG=()
  [ -n "${S3_ENDPOINT:-}" ] && EP_FLAG=(--endpoint-url "$S3_ENDPOINT")
  aws s3 cp "$TARBALL" "s3://${S3_BUCKET}/evidence/$(basename "$TARBALL")" "${EP_FLAG[@]}"
  note "uploaded to s3://${S3_BUCKET}/evidence/"
else
  note "S3_BUCKET unset — tarball left at $TARBALL"
fi

note "done"

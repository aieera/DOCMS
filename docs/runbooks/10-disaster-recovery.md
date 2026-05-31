# Disaster Recovery Runbook

**Owner:** Platform / SRE
**Last rehearsed:** never (see §Rehearsal below — overdue, FIX-8 status)
**RTO/RPO targets:** see per-store table below

> **FIX-8 status (2026-05-31).** Bucket versioning + automated
> restore verification have landed:
>
> - `deploy/helm/vaultdms/templates/jobs/bucket-init.yaml` — Helm
>   post-install hook runs `mc version enable` on every content
>   bucket (optional GOVERNANCE object-lock via
>   `s3BucketInit.retention.enabled`). Same wiring is in dev
>   docker-compose `minio-init`.
> - `deploy/helm/vaultdms/templates/cronjobs/backup-verify.yaml` —
>   weekly CronJob fetches the latest pg_dump, restores into a
>   scratch DB, asserts row counts on `documents` /
>   `document_versions` / `audit_events`. Failure pages oncall.
>
> Still deferred (ops cycles, not code):
>
> - **Continuous Postgres backup.** Daily pg_dump = ~24h RPO; the
>   5min RPO claimed below cannot be met without WAL shipping
>   (pgBackRest / wal-g) or CNPG-operator continuous backup
>   (`useOperator: true` in values.yaml is configured but gated
>   off).
> - **DR rehearsal.** First rehearsal under the new wiring should
>   commit a `docs/runbooks/dr-rehearsals/YYYY-MM-DD.md` step-by-
>   step log.

## Scope

This runbook covers recovery from loss of a primary region or a
catastrophic data-store failure. Smaller incidents (single pod crash,
slow query, one node down) are covered by service-specific runbooks
and the [SLO pause runbook](../slo/pause-runbook.md).

Events in scope:

- Complete loss of the primary AWS region.
- Postgres primary + all read replicas unavailable.
- Object-store (S3) bucket deletion or corruption.
- OpenSearch cluster total loss.
- NATS JetStream data loss.
- KMS CMK accidentally scheduled for deletion / region-wide KMS outage.

## RTO / RPO targets

| Data store | RPO (data loss) | RTO (time to recover) | Backup mechanism |
|---|---|---|---|
| Postgres (tenants, auth, metadata) | **5 min** | **1 h** | WAL shipping to cross-region S3 + 4h base backups |
| Object store (document blobs) | **0** | **30 min** | S3 cross-region replication (versioned) |
| OpenSearch (search index) | rebuildable | **2 h** | No backup — rebuild from Postgres + object store |
| NATS JetStream | **1 min** | **15 min** | JetStream mirror stream in secondary region |
| Redis (session + cache) | acceptable loss | **5 min** | No backup — all data is cache or session, users re-auth |
| KMS CMKs | **0** (MRKs) | **immediate** | Multi-region keys; deletion scheduled with 30d window |
| Temporal history | **15 min** | **1 h** | Cross-region Cassandra replication |

## Secondary region standby state

- **warm** — Postgres, NATS, Temporal, object store all have live
  replicas in `us-east-2` (primary: `us-east-1`). DNS + traffic flip is
  manual.
- Application pods are deployed but scaled to 0 in the secondary
  region; scaling to target takes ~5 min.

## Recovery procedures

### Total primary region loss

1. **Incident commander** declares DR. Post to #incidents with the
   DR-START template. Notify customers via status page (template in
   `docs/runbooks/templates/status-page-dr.md` — Wave 14.3b).
2. **Verify the primary is actually gone.** Check AWS Health Dashboard
   and regional status. Do not fail over for a partial outage — split
   brain costs more than the outage.
3. **Promote Postgres secondary:**
   ```bash
   # On the standby cluster
   aws rds promote-read-replica --db-instance-identifier vaultdms-prod-us-east-2
   # Verify no lag:
   psql -c "SELECT pg_last_wal_replay_lsn();"
   ```
4. **Flip NATS mirror to source:**
   ```bash
   nats stream edit AUDIT --config=/etc/nats/dr-source.json
   ```
5. **Scale app pods:**
   ```bash
   kubectl -n vaultdms scale deploy --replicas=<target> -l tier=api
   ```
6. **Flip DNS.** Route 53 weighted policy: shift 100% to
   `us-east-2`. Takes 60s with 30s TTL.
7. **Verify:** hit `/api/v1/health` on the new primary, log in as the
   DR test user, upload a document, search for it.
8. **Communicate** — status page update every 15 min until green.

### Postgres primary loss only

Same steps 3, 7, 8 from above. Skip region flip.

### Object-store corruption / deletion

1. Stop writes immediately (`kubectl scale deploy storage --replicas=0`).
2. Identify affected buckets/objects via audit events:
   ```sql
   SELECT * FROM audit.events
   WHERE event_type IN ('document.deleted', 'storage.blob.deleted')
     AND occurred_at > NOW() - INTERVAL '24 hours';
   ```
3. Restore from versioned S3 replicas:
   ```bash
   aws s3api list-object-versions --bucket vaultdms-blobs \
       --prefix "tenant/$TENANT_ID/"
   # Restore a deleted version:
   aws s3api copy-object --bucket vaultdms-blobs \
       --copy-source "vaultdms-blobs/key?versionId=$VID" \
       --key "$KEY"
   ```
4. Re-scale storage service. Spot-check 10 random blobs are readable.

### OpenSearch cluster loss

Index is rebuildable, not backed up.

1. Spin up fresh cluster.
2. Run search service in indexer mode:
   ```bash
   kubectl -n vaultdms create job reindex-all \
       --from=cronjob/search-reindex -- --all-tenants
   ```
3. Serve degraded responses (`503 Retry-After: 3600`) until reindex is
   complete. Budget: ~2h for 10M documents, see Wave 13.2 load results.

### KMS key compromised or scheduled for deletion

1. **Cancel deletion immediately** (30d window):
   ```bash
   aws kms cancel-key-deletion --key-id $KEY_ID
   ```
2. If compromise suspected, rotate by creating a new CMK and re-wrapping
   all DEKs. The `services/storage/internal/rewrap` path is the
   production procedure — not a drill script. See Wave 11.7 /
   Wave 12.3 docs.
3. Audit-log every decrypt during the suspected-compromise window.

## Rehearsal

**Quarterly.** The runbook is worthless if the first real invocation
is the first time anyone runs the commands.

Rehearsal schedule:

- **Q1 2026 (2026-03-01 done)** — *never run; this is the first
  cycle.*
- **Q2 2026 (target: 2026-05-15)** — Postgres failover drill in
  staging. Measure actual RTO.
- **Q3 2026** — Full region failover in staging. Measure RTO/RPO.
- **Q4 2026** — Object-store corruption + restore drill.

Rehearsal log: `docs/runbooks/dr-rehearsal-YYYY-MM-DD.md` per drill.
Required contents: what ran, actual RTO/RPO measured, what failed
that the runbook predicted would work, runbook edits filed as PRs.

Rehearsal fail criteria — any one triggers a remediation backlog item:

- RTO exceeded by >2x.
- Anyone had to improvise a command not in the runbook.
- Data loss exceeded RPO.
- A runbook step fails because of stale commands, changed credentials,
  or missing access.

## Access and credentials

DR actions require the `sre-oncall` IAM role, which is break-glass
only — accessed via the PagerDuty escalation and logged separately.
Day-to-day operators don't hold DR credentials. See
`docs/runbooks/06-key-management.md` for credential rotation.

## Known gaps

- **Residency** — a tenant marked `region=eu-west-1` cannot fail over
  to `us-east-2` without a compliance decision. The residency service
  (Wave 11.7) gates this. During a DR event the residency check is
  enforced; EU tenants are unavailable until EU secondary exists.
  Tracked as Wave 14.3c.
- **Temporal workflow rollback** on partial failover — if DNS flips
  but some workers continued running against the old region for 60s,
  duplicate activities can fire. Mitigated by Temporal's idempotency
  tokens; not eliminated. Document in post-rehearsal.
- **No DR for the secondary region itself.** Loss of both `us-east-1`
  and `us-east-2` is out of scope for this runbook. A third-region
  standby is Wave 15+.

## Pointers

- RTO/RPO per service: this file, above.
- Cross-region KMS: [docs/runbooks/06-key-management.md](06-key-management.md)
- Regional KEKs: [docs/audit/remediation/18d-wave11.7-regional-keks.md](../audit/remediation/18d-wave11.7-regional-keks.md)
- Status page templates: `docs/runbooks/templates/` (Wave 14.3b)

# Per-tenant restore

Recover ONE tenant's state without touching any other tenant. Use
cases:

- Customer accidentally hard-disposed a tenant they wanted to keep.
- Compliance-triggered restore of an archived tenant for legal hold.
- Post-incident recovery of a single tenant whose data was corrupted
  by a bug, while the rest of the platform was unaffected.

Full cluster restore is a separate procedure (see
[../10-disaster-recovery.md](../10-disaster-recovery.md)).

## Prerequisites

- Tenant id (`organizations.id`) and slug.
- Approximate target timestamp to restore to (RPO min: 5 min via PITR).
- Operator must have:
  - `psql` superuser on the primary Postgres.
  - MinIO / S3 console access with read on backups bucket.
  - Qdrant admin access for `dm_vectors` collection.
  - OpenSearch admin for `dms_documents` index.
  - KMS console access (AWS KMS / Vault Transit) to cancel any
    scheduled CMK deletion.

## Ordering

The order below is load-bearing — reversing steps creates dangling
references (doc row pointing at a blob id that doesn't exist yet)
and RLS admits zero rows until tenant_keks is populated.

1. **Cancel any scheduled CMK deletion** — if the tenant was
   hard-disposed, `tenant_keks.retired_at` is non-NULL. Check
   `dms-admin kms list --tenant <id>`; if a deletion is scheduled,
   run `aws kms cancel-key-deletion` (or Vault equivalent) BEFORE
   any data restore — otherwise the KEK disappears during the
   restore window and every blob becomes undecryptable.

2. **Restore `tenant_keks` rows** — the PITR snapshot contains
   every tenant's KEK alias history. Select rows for the target
   tenant + insert into current Postgres:

   ```sql
   INSERT INTO tenant_keks (tenant_id, version, alias, created_at, retired_at)
   SELECT tenant_id, version, alias, created_at, NULL  -- un-retire
     FROM aux.tenant_keks_snapshot
    WHERE tenant_id = :tenant;
   ```

3. **Restore `organizations` + `users` + `workspaces` + `folders`
   + `documents` + `versions`** — in that order; each has a FK to
   the prior. Scope each restore to `tenant_id = :tenant`:

   ```sql
   INSERT INTO organizations (...) SELECT ... FROM aux.orgs_snapshot WHERE id = :tenant;
   INSERT INTO users         SELECT ... FROM aux.users_snapshot      WHERE tenant_id = :tenant;
   -- etc, respecting FK order
   ```

4. **Restore object blobs** — for every `content_blobs` row in the
   restore set, `mc cp backups/{region}-hot/{tenant}/... live/{region}-hot/{tenant}/...`.
   The encrypted bytes are reusable because the KEK was restored in
   step 2.

5. **Restore search index** — `POST /_reindex` in OpenSearch from
   the per-tenant routing filter. Alternative: re-publish every
   `dms.document.created.v1` for the tenant from audit_events; the
   search service's consumer will rebuild the index.

6. **Restore vector collection** — Qdrant backup is per-collection,
   not per-tenant. Either:
   - Restore the full `dm_vectors` collection and accept that the
     tenant's neighbours see their own vectors (payload filter
     still enforces isolation at query time), OR
   - Delete all Qdrant points with `payload.tenant_id = :tenant`
     and regenerate from source docs via intelligence service.

7. **Republish outbox events** — `dms.tenant.restored.v1` (new
   event; needs adding to `pkg/events.DefaultStreams` subject set)
   so downstream consumers (notifications, analytics, SOC) learn
   the tenant is back.

## Verification

- `SELECT count(*) FROM documents WHERE tenant_id = :tenant;` matches
  the expected count from the source snapshot.
- Search returns hits for a known term scoped to the tenant.
- Qdrant semantic query for a known phrase returns a doc from the
  tenant.
- User can log in (tenant KEK available → session decrypt works).
- A known document's content download succeeds + bytes match a
  pre-hashed checksum.

## Time budget

- 1-tenant restore with ≤10k docs: **≤30 minutes** (Postgres PITR
  dominates; blob copy is parallel).
- 1-tenant restore with ≤1M docs: **≤4 hours** (OpenSearch reindex
  becomes critical path).

## Known gaps

- No dedicated "restore this tenant" tool today. Every step is a
  manual `psql` + `mc` + `curl` sequence. Backlog: a `dms-admin
  restore-tenant --tenant <id> --as-of <timestamp>` subcommand that
  orchestrates steps 2-7 with per-step rollback.
- Qdrant per-tenant restore is coarse. If per-tenant collections
  land (the `isolated-vector-store` opt-in already tracked in the
  ledger), restore becomes drop + reload the one collection.

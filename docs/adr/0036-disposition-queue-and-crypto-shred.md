# ADR 0036 — Disposition queue, approval, and crypto-shred

**Status:** Proposed · **Date:** 2026-04-26 · **Builds on:** Wave 8.4 retention sweeper (`services/document/internal/service/retention.go`), ADR 0026 per-region KEKs (envelope encryption invariants).

## Context

The Wave 8.4 retention sweeper (`SweepRetention`) finds documents whose `created_at + retain_days < now()` and immediately calls `UpdateLifecycle(action=archive|dispose)`. That is wrong for compliance-grade disposition for three reasons:

1. **No human approval.** Most regulated tenants (HIPAA, GDPR Art. 17 Right to Erasure, SOX 7-year retention) require a named reviewer to attest that each disposition is correct *before* destruction. A clock-driven `dispose` is not "right to erasure" — it's an unaccountable batch delete.
2. **`dispose` today does nothing.** `model.ActionDispose` is a state-machine transition only — the document moves to `lifecycle_state='disposed'` but the blob, the DEK, and every version stay in MinIO/S3 forever. From the user's perspective the document is "deleted"; from a forensics or subpoena perspective it is fully recoverable. That is the worst-of-both-worlds state.
3. **No undo window.** Even with approval, accidental disposition is a real failure mode (wrong policy filter, wrong document class). The current path has no staging where a 24h "this will be destroyed at 3am tomorrow" period exists.

The brief in `final.md` §9.4 calls for a "disposition queue" with reviewer approval and **crypto-shred** as the destruction primitive. Crypto-shred is the right primitive in our envelope-encryption model: each blob has a per-blob DEK wrapped by a per-region KEK (ADR 0026). Destroying the wrapped DEK row makes the ciphertext mathematically unrecoverable without touching the blob at all — important for legal-hold-adjacent cases where the *bytes* may be replicated to backups we cannot reach.

## Decision

### Schema (migration `000016_disposition_queue`)

```sql
CREATE TABLE disposition_candidates (
    tenant_id        UUID NOT NULL REFERENCES organizations(id),
    id               UUID NOT NULL DEFAULT gen_random_uuid(),
    document_id      UUID NOT NULL,
    policy_id        UUID NOT NULL REFERENCES retention_policies(id),
    proposed_action  TEXT NOT NULL CHECK (proposed_action IN ('archive', 'dispose')),
    proposed_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- queued | approved | rejected | executed | superseded
    --   superseded = a newer policy run replaced this row with a different
    --   action (rare; happens when a policy is edited mid-window).
    status           TEXT NOT NULL DEFAULT 'queued',
    reviewer_id      UUID,
    decided_at       TIMESTAMPTZ,
    decided_reason   TEXT,
    -- Earliest moment the executor will act after approval. Default
    -- 24h from approval; the soak window gives operations a chance
    -- to spot a bad policy run.
    execute_after    TIMESTAMPTZ,
    executed_at      TIMESTAMPTZ,
    -- Hash of the document's content_blob_id at proposal time. If the
    -- doc has a new version uploaded between propose and execute, the
    -- candidate is auto-superseded — we never crypto-shred a version
    -- the user wrote *after* the policy decided.
    proposed_blob_id UUID,
    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, document_id) REFERENCES documents(tenant_id, id),
    FOREIGN KEY (tenant_id, policy_id) REFERENCES retention_policies(tenant_id, id)
);
CREATE UNIQUE INDEX idx_disposition_candidates_active
    ON disposition_candidates(tenant_id, document_id)
    WHERE status IN ('queued', 'approved');
CREATE INDEX idx_disposition_candidates_review_queue
    ON disposition_candidates(tenant_id, status, proposed_at)
    WHERE status IN ('queued', 'approved');
```

The unique partial index makes "one open candidate per document" a database invariant — a second sweep that finds the same doc gets `ON CONFLICT DO NOTHING`, not a duplicate row.

### Flow

```
sweeper            queue            reviewer          executor
───────            ─────            ────────          ────────
discover doc  →   INSERT
                  status=queued
                                →   UI shows queue
                                    PATCH approved
                                    execute_after = now+24h
                                                  →    cron picks up
                                                       approved + execute_after≤now
                                                       crypto-shred + audit
                                                       status=executed
```

Held documents (under_legal_hold OR hold_count > 0) are filtered at sweeper time, before insert. Already-queued candidates whose document is *later* placed on hold are auto-rejected by the executor's pre-flight check, never destroyed.

### Crypto-shred = "null the wrapped DEK on the blob row"

The wrapped DEK lives on `content_blobs.encrypted_dek` (bytea), which is **owned by the storage service**, not the document service. The shred is therefore a cross-service operation: the document executor calls a new gRPC `Storage.ShredBlobs(tenant_id, blob_ids[])` that performs the DB-side zeroing inside its own tenant tx. From the document executor's perspective:

```
Document executor                          Storage service
─────────────────                          ───────────────
BEGIN tx (documents schema)
SELECT version blob_ids for doc
ShredBlobs(blob_ids) ────────────────────► BEGIN tx (storage schema)
                                           UPDATE content_blobs
                                              SET encrypted_dek = NULL,
                                                  dek_nonce     = NULL,
                                                  shredded_at   = now()
                                            WHERE tenant_id = $1 AND id = ANY($2)
                                           emit audit outbox: dms.blob.shredded.v1
                                           COMMIT
                                           ◄──── ok
UPDATE documents SET lifecycle_state='disposed', shredded_at=now()
emit audit outbox: dms.disposition.executed.v1
COMMIT
(best-effort, async) Storage.DeleteBlobs(blob_ids) → S3 DELETE
```

We zero **every version's blob**, not just the current one — partial shred would leave older versions decryptable, defeating the point. The two-phase commit is *not* atomic across services; if the document tx fails after `ShredBlobs` succeeded, we end up with a shredded blob whose document still says `active`. This is the safe direction of inconsistency: the data is already unrecoverable, so a follow-up reconcile can advance the document state without risk. The opposite ordering (document first, blob second) could leave a "disposed" document with recoverable bytes — unacceptable.

We deliberately commit *before* the S3 delete. The S3 object without its DEK is unrecoverable ciphertext — leaving it for a few minutes (or forever, on backup tape) is fine. Trying to make S3-delete transactional with the DB would re-introduce the dual-write problem. The audit event records the shred; the S3 cleanup is opportunistic.

Two storage-side migrations land alongside this: `content_blobs.shredded_at TIMESTAMPTZ` and a `dek IS NULL` consistency CHECK on the `shredded_at` column. The storage service's existing scanners already pointer-scan `encrypted_dek`, so a NULL value reads cleanly; download paths must learn to translate NULL → 410 Gone instead of a 500.

We deliberately commit *before* the S3 delete. The S3 object without its DEK is unrecoverable ciphertext — leaving it for a few minutes (or forever, on backup tape) is fine. Trying to make S3-delete transactional with the DB would re-introduce the dual-write problem. The audit event records the shred; the S3 cleanup is opportunistic.

### Roles and gating

- **Propose** (sweeper): system role, OPA `retention_driven` (existing).
- **Review queue read**: `compliance_officer | admin | owner`.
- **Approve / reject**: `compliance_officer | owner`. `admin` cannot approve — separation-of-duties: the same person who configured the retention policy should not solo-approve its destructions.
- **Execute** (cron): system role, OPA `disposition_executor` (new rule).

### Notification

Approval queue depth >0 emits `dms.notify.disposition.review.v1` to the outbox on the first transition from empty→non-empty per tenant per hour (rate-limited so a 10k-row sweep doesn't spam compliance officers). Notification service fans to compliance_officer + owner role members per the Wave 15 `dms.notify.>` pattern.

### Endpoints

```
GET    /api/v1/admin/disposition/candidates?status=queued     list
GET    /api/v1/admin/disposition/candidates/{id}              detail (incl. doc title, policy name)
POST   /api/v1/admin/disposition/candidates/{id}/approve      compliance_officer|owner
POST   /api/v1/admin/disposition/candidates/{id}/reject       compliance_officer|owner; reason required
POST   /internal/v1/disposition/execute                       cron-driven; idempotent
```

## What we did not do

- **No "instant disposition" override.** The 24h soak after approval is non-negotiable for regulated tenants; an `execute_after_seconds=0` flag would invite footguns. If an operator needs immediate action they can call the existing `/admin/documents/{id}` DELETE path (which has its own audit trail and is gated to `owner`).
- **No per-document reviewer assignment.** The queue is tenant-flat. A future enhancement could assign by `document_class` or workspace, but the v1 review surface is "any compliance_officer can act on any candidate".
- **No retention policy version pinning on the candidate row.** If a policy is edited between propose and execute, the candidate is auto-superseded by the next sweep so the destruction always reflects the most recent policy text. Audit trail captures both versions.
- **No automatic blob delete in the same tx as DEK shred.** See above — the shred is the cryptographic event of record; the S3 delete is hygiene.
- **No move to a separate `services/retention/` module.** All disposition logic lives in `services/document/internal/service/disposition.go` (new file) alongside the existing retention sweeper. The blast radius is one service.

## Consequences

**Positive**
- Compliance-grade approval trail. Each destruction has a named reviewer, a reason, a 24h reversibility window, and an audit hash chain entry.
- Crypto-shred makes destruction *cryptographically* irreversible without depending on S3 delete propagation, backup retention, or replica timing.
- Database-enforced "one open candidate per document" prevents duplicate destruction races.
- Held documents are doubly-protected: filtered at sweep time, re-checked at execution time.

**Negative**
- Disposition latency goes from "minutes after sweep" to "24h+ after sweep" by design. Tenants relying on the old behavior to meet a regulatory deadline must approve faster, not destruction-faster.
- Two-table coordination (`disposition_candidates` and `content_blob_keys.wrapped_dek`) — a manual `DELETE FROM disposition_candidates` would orphan the audit invariant. Documented in the runbook; the table is owner-restricted.
- Extra cron tick (the executor) and an extra outbox subject (`dms.disposition.*`). Both negligible operationally.
- The S3-delete hygiene path is best-effort; a permanent S3 outage means undeleted ciphertext objects accumulate. The reconcile worker in `services/storage` already sweeps orphans for upload reconciliation; extend it to disposition-shredded blobs.

## References

- Wave 8.4 retention sweeper — [`services/document/internal/service/retention.go`](../../services/document/internal/service/retention.go)
- ADR 0026 per-region KEK masters — envelope encryption invariants
- ADR 0027 acknowledgement attestation chain — pattern reference for the per-decision audit trail
- Blueprint §9.4 retention & disposition

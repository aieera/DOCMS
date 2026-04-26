-- Migration 000016 — Disposition queue (ADR 0036).
--
-- Wave 8.4's SweepRetention applies retention actions immediately:
-- a clock-driven dispose call destroys nothing today (state-only) but
-- offers no human approval, no soak window, and no audit trail of who
-- decided. This migration introduces the queue + approval substrate.
--
-- One row per (tenant, document) under disposition consideration. The
-- partial unique index makes "one open candidate per document" a
-- database invariant — repeat sweeps over the same doc are ON CONFLICT
-- DO NOTHING, no duplicate review work for compliance.
--
-- Held documents never reach this table — the sweeper filters them
-- before insert and the executor re-checks at action time. See ADR 0036
-- §"Held documents are doubly-protected".

CREATE TABLE disposition_candidates (
    tenant_id        UUID NOT NULL REFERENCES organizations(id),
    id               UUID NOT NULL DEFAULT gen_random_uuid(),
    document_id      UUID NOT NULL,
    policy_id        UUID NOT NULL,

    -- Action proposed by the sweeper. Re-validated at execute time;
    -- the executor refuses to dispose a doc that's now on hold.
    proposed_action  TEXT NOT NULL CHECK (proposed_action IN ('archive', 'dispose')),
    proposed_at      TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- queued      → awaiting reviewer
    -- approved    → reviewer said yes; executor will act after execute_after
    -- rejected    → reviewer said no; row is terminal, audit-only
    -- executed    → executor crypto-shredded (or archived); terminal
    -- superseded  → a newer policy run replaced this row with a different
    --               action (e.g. policy edited mid-window). Terminal.
    status           TEXT NOT NULL DEFAULT 'queued'
                       CHECK (status IN ('queued', 'approved', 'rejected', 'executed', 'superseded')),

    reviewer_id      UUID,
    decided_at       TIMESTAMPTZ,
    decided_reason   TEXT,

    -- Earliest moment the executor will act after approval. Default
    -- now()+24h at approval time; the soak window gives operations
    -- a chance to spot a bad policy run before destruction commits.
    execute_after    TIMESTAMPTZ,
    executed_at      TIMESTAMPTZ,

    -- The current_version's content_blob_id at proposal time. If a
    -- new version is uploaded between propose and execute, the
    -- candidate is auto-superseded — we never crypto-shred a version
    -- the user wrote *after* the policy decided this doc was disposable.
    -- Nullable because some policies (archive) don't need it; the
    -- dispose path enforces NOT NULL via service code.
    proposed_blob_id UUID,

    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),

    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, document_id) REFERENCES documents(tenant_id, id),
    FOREIGN KEY (tenant_id, policy_id)   REFERENCES retention_policies(tenant_id, id),

    -- Sanity invariants enforced by the DB:
    --   approved/rejected rows have a reviewer + decided_at
    --   executed rows have an executed_at
    CHECK ((status NOT IN ('approved', 'rejected'))
           OR (reviewer_id IS NOT NULL AND decided_at IS NOT NULL)),
    CHECK ((status <> 'executed') OR (executed_at IS NOT NULL))
);

-- One open candidate per document. A second sweep over the same doc
-- gets ON CONFLICT DO NOTHING — no duplicate review work.
CREATE UNIQUE INDEX idx_disposition_candidates_active
    ON disposition_candidates(tenant_id, document_id)
    WHERE status IN ('queued', 'approved');

-- Review queue read path (UI lists status=queued, executor scans
-- status=approved AND execute_after <= now()).
CREATE INDEX idx_disposition_candidates_review_queue
    ON disposition_candidates(tenant_id, status, proposed_at)
    WHERE status IN ('queued', 'approved');

-- updated_at maintenance — same trigger pattern as every other
-- updated_at column in this schema (see 000001 documents).
CREATE TRIGGER update_disposition_candidates_updated_at
    BEFORE UPDATE ON disposition_candidates
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

-- ---- RLS ------------------------------------------------------------
ALTER TABLE disposition_candidates ENABLE  ROW LEVEL SECURITY;
ALTER TABLE disposition_candidates FORCE   ROW LEVEL SECURITY;

CREATE POLICY disposition_candidates_tenant_isolation ON disposition_candidates
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);

CREATE POLICY disposition_candidates_tenant_isolation_insert ON disposition_candidates
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

-- ---- shredded_at on documents --------------------------------------
-- Records the moment crypto-shred happened. Distinct from deleted_at
-- (soft delete, recoverable) and lifecycle_state='disposed' (state).
-- A row with shredded_at IS NOT NULL is cryptographically destroyed —
-- the matching content_blobs.encrypted_dek is NULL and download paths
-- must return 410 Gone.
ALTER TABLE documents
    ADD COLUMN shredded_at TIMESTAMPTZ;

CREATE INDEX idx_documents_shredded
    ON documents(tenant_id, shredded_at)
    WHERE shredded_at IS NOT NULL;

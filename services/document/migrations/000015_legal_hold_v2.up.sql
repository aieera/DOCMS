-- Migration 000015 — Legal hold §9.3 closure.
--
-- Augments the Wave 8.2 schema (legal_holds + legal_hold_documents)
-- with three §9.3 primitives that the original brief left out:
--
--   1. legal_hold_custodians — explicit custodian list per hold, with
--      acknowledgement timestamps. Powers the /legal-holds/my inbox
--      and the dms.notify.legalhold.applied/released.v1 fan-out.
--
--   2. legal_hold_targets — non-document targets (folder, workspace,
--      saved_search). Folder + workspace already worked indirectly
--      via the existing legal_hold_documents binding, but explicit
--      targets are needed for "saved-search holds with auto-include
--      on new matching docs" (the auto-include worker is Wave-sized
--      and explicitly out of scope; the table is the API contract).
--
--   3. legal_hold_events — append-only hash-chained per-hold log,
--      mirroring acknowledgement_events. Required for the new
--      GET /verify-chain endpoint. Decoupled from the existing
--      generic outbox so a single hold's chain is independently
--      verifiable in court.
--
-- Plus documents.hold_count INT alongside the existing
-- under_legal_hold BOOLEAN. The boolean stays as the fast-path index
-- for enforcement queries; the counter is incremented per active
-- hold so overlapping holds compose cleanly (e.g. a folder-hold +
-- a doc-hold on the same row → hold_count=2; releasing one keeps
-- under_legal_hold=true until both clear).

CREATE TABLE legal_hold_custodians (
    tenant_id        UUID NOT NULL REFERENCES organizations(id),
    id               UUID NOT NULL DEFAULT gen_random_uuid(),
    hold_id          UUID NOT NULL,
    user_id          UUID NOT NULL,
    notified_at      TIMESTAMPTZ,
    acknowledged_at  TIMESTAMPTZ,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id),
    UNIQUE (tenant_id, hold_id, user_id),
    FOREIGN KEY (tenant_id, hold_id) REFERENCES legal_holds(tenant_id, id) ON DELETE CASCADE,
    FOREIGN KEY (tenant_id, user_id) REFERENCES users(tenant_id, id)
);
CREATE INDEX idx_legal_hold_custodians_user ON legal_hold_custodians(tenant_id, user_id);
CREATE INDEX idx_legal_hold_custodians_hold ON legal_hold_custodians(tenant_id, hold_id);
ALTER TABLE legal_hold_custodians ENABLE  ROW LEVEL SECURITY;
ALTER TABLE legal_hold_custodians FORCE   ROW LEVEL SECURITY;
CREATE POLICY legal_hold_custodians_iso ON legal_hold_custodians
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY legal_hold_custodians_iso_insert ON legal_hold_custodians
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

CREATE TABLE legal_hold_targets (
    tenant_id          UUID NOT NULL REFERENCES organizations(id),
    id                 UUID NOT NULL DEFAULT gen_random_uuid(),
    hold_id            UUID NOT NULL,
    -- Document targets continue to live in legal_hold_documents — that
    -- table is the canonical fast-path lookup and many code paths
    -- already join against it. This table covers the *other* target
    -- kinds the §9.3 brief calls out.
    target_type        TEXT NOT NULL CHECK (target_type IN ('folder', 'workspace', 'saved_search')),
    target_id          UUID,
    -- Saved-search targets carry the JSON body that defines the
    -- query. Auto-inclusion of newly-matching docs is performed by
    -- a separate worker (deferred — see ADR 0035 follow-ups).
    search_query_json  JSONB,
    applied_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, hold_id) REFERENCES legal_holds(tenant_id, id) ON DELETE CASCADE,
    CHECK ((target_type = 'saved_search' AND search_query_json IS NOT NULL AND target_id IS NULL)
        OR (target_type IN ('folder', 'workspace') AND target_id IS NOT NULL))
);
CREATE INDEX idx_legal_hold_targets_hold ON legal_hold_targets(tenant_id, hold_id);
ALTER TABLE legal_hold_targets ENABLE  ROW LEVEL SECURITY;
ALTER TABLE legal_hold_targets FORCE   ROW LEVEL SECURITY;
CREATE POLICY legal_hold_targets_iso ON legal_hold_targets
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY legal_hold_targets_iso_insert ON legal_hold_targets
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

CREATE TABLE legal_hold_events (
    tenant_id     UUID NOT NULL REFERENCES organizations(id),
    id            UUID NOT NULL DEFAULT gen_random_uuid(),
    hold_id       UUID NOT NULL,
    sequence      BIGINT NOT NULL,
    -- One of: applied | released | target_added | target_removed |
    -- custodian_added | custodian_acknowledged | updated.
    event_type    TEXT NOT NULL,
    actor_id      UUID,
    payload       JSONB NOT NULL,
    -- prev_hash is the SHA-256 over the previous row's self_hash + the
    -- per-tenant HMAC secret. self_hash = SHA-256(prev_hash || event
    -- bytes || tenant secret). The verify-chain endpoint walks the
    -- table per hold and recomputes; mismatches surface as 409 with
    -- the broken sequence number.
    prev_hash     BYTEA NOT NULL,
    self_hash     BYTEA NOT NULL,
    occurred_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id),
    UNIQUE (tenant_id, hold_id, sequence),
    FOREIGN KEY (tenant_id, hold_id) REFERENCES legal_holds(tenant_id, id) ON DELETE CASCADE
);
CREATE INDEX idx_legal_hold_events_hold ON legal_hold_events(tenant_id, hold_id, sequence);
ALTER TABLE legal_hold_events ENABLE  ROW LEVEL SECURITY;
ALTER TABLE legal_hold_events FORCE   ROW LEVEL SECURITY;
CREATE POLICY legal_hold_events_iso ON legal_hold_events
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY legal_hold_events_iso_insert ON legal_hold_events
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

ALTER TABLE documents
    ADD COLUMN hold_count INTEGER NOT NULL DEFAULT 0
        CHECK (hold_count >= 0);

-- Backfill: existing rows with under_legal_hold=true → hold_count=1.
-- Multi-hold composition starts from this commit forward.
UPDATE documents SET hold_count = 1 WHERE under_legal_hold = TRUE;

-- ADR 0064 — Approval routing patterns (recall + tenant-wide
-- delegations + manager-hierarchy escalation).
--
-- Two new things:
--   1. workflow_delegations  — tenant-wide forward rules with time bounds
--   2. users.manager_id      — org hierarchy for escalation strategy=manager
--
-- The existing workflow_definitions / workflow_instances /
-- workflow_tasks tables stay; their semantics widen via the JSON
-- schema (ADR 0064 §"Definition shape") rather than DDL changes.

-- ---- users.manager_id ---------------------------------------------------
ALTER TABLE users
    ADD COLUMN IF NOT EXISTS manager_id UUID;

-- Self-referential FK scoped to the tenant. Manager must live in
-- the same tenant — the composite (tenant_id, manager_id) is the
-- thing we want to enforce.
ALTER TABLE users
    DROP CONSTRAINT IF EXISTS users_manager_fk;
ALTER TABLE users
    ADD CONSTRAINT users_manager_fk
        FOREIGN KEY (tenant_id, manager_id)
        REFERENCES users(tenant_id, id)
        ON DELETE SET NULL;

-- Index for the up-the-chain walk used by escalation. Only rows
-- with a manager are useful.
CREATE INDEX IF NOT EXISTS idx_users_manager
    ON users (tenant_id, manager_id) WHERE manager_id IS NOT NULL;


-- ---- workflow_delegations -----------------------------------------------
-- A delegator says "every approval task assigned to me between
-- starts_at and ends_at goes to delegate_id instead". The
-- ResolveAssignee activity reads this on task creation and writes
-- BOTH ids into the audit row so the chain is visible later.
CREATE TABLE IF NOT EXISTS workflow_delegations (
    tenant_id      UUID         NOT NULL REFERENCES organizations(id),
    id             UUID         NOT NULL DEFAULT gen_random_uuid(),
    delegator_id   UUID         NOT NULL,
    delegate_id    UUID         NOT NULL,
    -- Overlapping windows ARE allowed (delegator goes on vacation
    -- twice in March). Resolver picks the most-recently-created
    -- active window.
    starts_at      TIMESTAMPTZ  NOT NULL,
    ends_at        TIMESTAMPTZ  NOT NULL,
    reason         TEXT,
    -- ADR 0064 — cycles (a → b → a) are rejected at insert time.
    -- We don't enforce that in the DDL because it's a graph
    -- property; the service-layer check runs in the same tx.
    revoked_at     TIMESTAMPTZ,
    created_at     TIMESTAMPTZ  NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id),
    CONSTRAINT workflow_delegations_window CHECK (ends_at > starts_at),
    CONSTRAINT workflow_delegations_no_self CHECK (delegator_id <> delegate_id),
    FOREIGN KEY (tenant_id, delegator_id) REFERENCES users(tenant_id, id) ON DELETE CASCADE,
    FOREIGN KEY (tenant_id, delegate_id)  REFERENCES users(tenant_id, id) ON DELETE CASCADE
);

-- Hot-path query for the resolver: "active delegations for this user
-- right now, newest first".
CREATE INDEX IF NOT EXISTS idx_workflow_delegations_active
    ON workflow_delegations (tenant_id, delegator_id, created_at DESC)
    WHERE revoked_at IS NULL;

ALTER TABLE workflow_delegations ENABLE ROW LEVEL SECURITY;
ALTER TABLE workflow_delegations FORCE  ROW LEVEL SECURITY;
DROP POLICY IF EXISTS workflow_delegations_tenant_isolation        ON workflow_delegations;
DROP POLICY IF EXISTS workflow_delegations_tenant_isolation_insert ON workflow_delegations;
CREATE POLICY workflow_delegations_tenant_isolation ON workflow_delegations
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY workflow_delegations_tenant_isolation_insert ON workflow_delegations
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

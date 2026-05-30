-- Phase 2 of the workflow-templates feature: per-definition ACL.
-- Mirrors folders/000061; the table lives under document's schema
-- because workflow_definitions was created in document's 000001
-- (run with: make migrate-up SERVICE=document).
--
-- Adds:
--   1. workflow_definitions.visibility — 'shared' (default, every
--      workspace member can see + launch) | 'private' (owner + grants
--      only). Default 'shared' so existing definitions keep behaving
--      as they always have.
--   2. workflow_definitions.owner_id — populated for private
--      definitions at flip time. NULL allowed for shared rows so the
--      concept doesn't apply where it doesn't need to.
--   3. workflow_grants — definition-scoped ACL. (grantee_type,
--      grantee_id) pairs are unique per definition so a re-grant is
--      an idempotent no-op.

ALTER TABLE workflow_definitions
    ADD COLUMN visibility TEXT NOT NULL DEFAULT 'shared'
        CHECK (visibility IN ('shared', 'private')),
    ADD COLUMN owner_id   UUID;

ALTER TABLE workflow_definitions
    ADD CONSTRAINT workflow_definitions_tenant_id_owner_id_fkey
        FOREIGN KEY (tenant_id, owner_id)
        REFERENCES users(tenant_id, id);

-- Index on (tenant_id, owner_id) so "definitions I own" scans
-- cheaply for the templates list.
CREATE INDEX idx_workflow_definitions_owner
    ON workflow_definitions(tenant_id, owner_id)
    WHERE owner_id IS NOT NULL;

CREATE TABLE workflow_grants (
    tenant_id     UUID        NOT NULL REFERENCES organizations(id),
    id            UUID        NOT NULL DEFAULT gen_random_uuid(),
    workflow_id   UUID        NOT NULL,
    grantee_type  TEXT        NOT NULL CHECK (grantee_type IN ('user', 'group')),
    grantee_id    UUID        NOT NULL,
    granted_by    UUID,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id),
    UNIQUE (tenant_id, workflow_id, grantee_type, grantee_id),
    FOREIGN KEY (tenant_id, workflow_id) REFERENCES workflow_definitions(tenant_id, id) ON DELETE CASCADE,
    FOREIGN KEY (tenant_id, granted_by)  REFERENCES users(tenant_id, id)
);

CREATE INDEX idx_workflow_grants_workflow ON workflow_grants(tenant_id, workflow_id);
CREATE INDEX idx_workflow_grants_grantee  ON workflow_grants(tenant_id, grantee_type, grantee_id);

ALTER TABLE workflow_grants ENABLE ROW LEVEL SECURITY;
ALTER TABLE workflow_grants FORCE  ROW LEVEL SECURITY;
CREATE POLICY workflow_grants_tenant_isolation ON workflow_grants
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY workflow_grants_tenant_isolation_insert ON workflow_grants
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

GRANT SELECT, INSERT, UPDATE, DELETE ON workflow_grants TO dms_app;

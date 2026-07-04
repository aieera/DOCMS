-- Workspace templates (ADR 0118): a reusable folder-tree definition
-- (placeholder docs + metadata defaults + folder grants) provisioned
-- into a workspace via POST /api/v1/templates/{id}/provision. The
-- definition is a JSONB tree validated in the service layer; provision
-- walks it inside ONE tenant tx so a half-created project can't exist.

CREATE TABLE IF NOT EXISTS workspace_templates (
    tenant_id   UUID NOT NULL REFERENCES organizations(id),
    id          UUID NOT NULL DEFAULT gen_random_uuid(),
    name        TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    definition  JSONB NOT NULL,
    created_by  UUID NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS idx_workspace_templates_name
    ON workspace_templates (tenant_id, name);

ALTER TABLE workspace_templates ENABLE ROW LEVEL SECURITY;
ALTER TABLE workspace_templates FORCE ROW LEVEL SECURITY;
CREATE POLICY workspace_templates_isolation ON workspace_templates
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid)
    WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

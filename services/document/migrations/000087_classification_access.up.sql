-- Classification-based access control (§8): gate document view/download/share
-- by a document sensitivity level + per-user clearance. The intelligence
-- compliance scan (PII/PHI, ADR 0054) is denormalised onto the documents row
-- so the read path can gate without a cross-table join, and a per-user
-- clearance attribute + tenant-defined classification->access rules drive the
-- OPA deny decision. Enforcement is opt-in per tenant (classification_gate_config).

-- Canonical sensitivity ladder: unclassified < internal < confidential < restricted.
-- '' means "not set" and ranks as the lowest (unclassified) — fail-closed for
-- clearance (an unset clearance can see only unclassified docs) and open for
-- classification (an unclassified doc needs no clearance).

ALTER TABLE documents
    ADD COLUMN IF NOT EXISTS security_classification TEXT NOT NULL DEFAULT ''
        CHECK (security_classification IN ('', 'unclassified', 'internal', 'confidential', 'restricted')),
    ADD COLUMN IF NOT EXISTS has_phi               BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS has_pii               BOOLEAN NOT NULL DEFAULT false,
    -- 'scan' (derived from compliance), 'manual' (admin-set), 'records'
    -- (declared record's security_classification). A manual/records marking is
    -- authoritative and the scan consumer must not lower it.
    ADD COLUMN IF NOT EXISTS classification_source TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_documents_security_classification
    ON documents(tenant_id, security_classification) WHERE deleted_at IS NULL;

-- Per-user clearance (tenant-scoped). Empty = no clearance granted.
ALTER TABLE users
    ADD COLUMN IF NOT EXISTS clearance TEXT NOT NULL DEFAULT ''
        CHECK (clearance IN ('', 'unclassified', 'internal', 'confidential', 'restricted'));

-- Tenant-defined classification -> access rules. Each row says: to perform
-- `action` on a document whose effective classification is >= min_classification,
-- the caller must hold at least `required_clearance`. The strictest matching
-- rule wins; with no rule, the default is required_clearance == classification
-- (identity ladder). action '*' matches any action.
CREATE TABLE IF NOT EXISTS classification_access_rules (
    tenant_id          UUID NOT NULL REFERENCES organizations(id),
    id                 UUID NOT NULL DEFAULT gen_random_uuid(),
    min_classification TEXT NOT NULL
        CHECK (min_classification IN ('unclassified', 'internal', 'confidential', 'restricted')),
    action             TEXT NOT NULL
        CHECK (action IN ('*', 'view', 'view_unredacted', 'download', 'share', 'edit', 'delete')),
    required_clearance TEXT NOT NULL
        CHECK (required_clearance IN ('unclassified', 'internal', 'confidential', 'restricted')),
    -- when true, this rule also applies to documents flagged has_phi regardless
    -- of their classification level (PHI is the DoD's canonical block case).
    applies_to_phi     BOOLEAN NOT NULL DEFAULT false,
    description        TEXT NOT NULL DEFAULT '',
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_by         UUID,
    PRIMARY KEY (tenant_id, id)
);

ALTER TABLE classification_access_rules ENABLE ROW LEVEL SECURITY;
ALTER TABLE classification_access_rules FORCE ROW LEVEL SECURITY;
CREATE POLICY classification_access_rules_isolation ON classification_access_rules
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);

-- Per-tenant enablement of classification gating. Absent row = disabled (the
-- read path pays nothing and behaves exactly as before).
CREATE TABLE IF NOT EXISTS classification_gate_config (
    tenant_id  UUID PRIMARY KEY REFERENCES organizations(id),
    enabled    BOOLEAN NOT NULL DEFAULT false,
    -- when true, a document flagged has_phi requires 'restricted' clearance even
    -- if no explicit rule matches — the always-on PHI backstop.
    phi_requires_restricted BOOLEAN NOT NULL DEFAULT true,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_by UUID
);

ALTER TABLE classification_gate_config ENABLE ROW LEVEL SECURITY;
ALTER TABLE classification_gate_config FORCE ROW LEVEL SECURITY;
CREATE POLICY classification_gate_config_isolation ON classification_gate_config
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);

-- Dynamic viewer watermark configuration (§5). Per-tenant defaults plus
-- per-classification overrides. The document service resolves the effective
-- style for a document, substitutes the viewer's identity into the template,
-- and hands the finished text to services/preview for stamping. Enforcement is
-- opt-in per tenant (enabled defaults false).

CREATE TABLE IF NOT EXISTS watermark_config (
    tenant_id    UUID PRIMARY KEY REFERENCES organizations(id),
    enabled      BOOLEAN NOT NULL DEFAULT false,
    -- Template tokens: {email} {timestamp} {ip} {tenant} {classification} {user_id}
    template     TEXT    NOT NULL DEFAULT '{email} · {timestamp} · {ip}',
    opacity      INT     NOT NULL DEFAULT 15  CHECK (opacity BETWEEN 0 AND 100),
    rotation_deg INT     NOT NULL DEFAULT 30  CHECK (rotation_deg BETWEEN -180 AND 180),
    tile         BOOLEAN NOT NULL DEFAULT true,
    font_size    INT     NOT NULL DEFAULT 28  CHECK (font_size BETWEEN 6 AND 200),
    color        TEXT    NOT NULL DEFAULT '#808080',
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_by   UUID
);

ALTER TABLE watermark_config ENABLE ROW LEVEL SECURITY;
ALTER TABLE watermark_config FORCE ROW LEVEL SECURITY;
CREATE POLICY watermark_config_isolation ON watermark_config
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);

-- Per-classification overrides. classification 'phi' is a pseudo-level matching
-- documents flagged has_phi (takes precedence over the security_classification
-- level). An override can force the watermark on (force=true) even when the
-- tenant default is off, and tune opacity/tiling; NULL opacity/tile inherit.
CREATE TABLE IF NOT EXISTS watermark_classification_overrides (
    tenant_id      UUID NOT NULL REFERENCES organizations(id),
    id             UUID NOT NULL DEFAULT gen_random_uuid(),
    classification TEXT NOT NULL
        CHECK (classification IN ('unclassified', 'internal', 'confidential', 'restricted', 'phi')),
    enabled        BOOLEAN NOT NULL DEFAULT true,
    opacity        INT     CHECK (opacity IS NULL OR opacity BETWEEN 0 AND 100),
    tile           BOOLEAN,
    force          BOOLEAN NOT NULL DEFAULT false,
    description    TEXT    NOT NULL DEFAULT '',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_by     UUID,
    PRIMARY KEY (tenant_id, id),
    UNIQUE (tenant_id, classification)
);

ALTER TABLE watermark_classification_overrides ENABLE ROW LEVEL SECURITY;
ALTER TABLE watermark_classification_overrides FORCE ROW LEVEL SECURITY;
CREATE POLICY watermark_overrides_isolation ON watermark_classification_overrides
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);

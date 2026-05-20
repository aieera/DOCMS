-- ADR 0098 — Zero-trust view-only share.
--
-- Two tables: the token row that authorizes a recipient to view a
-- specific (document, version) pair, and the telemetry stream the
-- viewer page POSTs every page-view / dwell / focus event back to.
-- Both RLS'd on the sender's tenant.

CREATE TABLE IF NOT EXISTS zt_share_tokens (
    tenant_id        uuid        NOT NULL REFERENCES organizations(id),
    token_id         uuid        NOT NULL,                       -- public ID in /zt/{token_id}/* URLs (uuidv7)
    token_hash       bytea       NOT NULL,                       -- sha256(token_id || pepper); look up by hash, not raw
    document_id      uuid        NOT NULL,
    version_id       uuid        NOT NULL,
    recipient_email  text        NOT NULL,                       -- burned into the watermark
    watermark_text   text        NOT NULL,                       -- final rendered string
    created_by       uuid        NOT NULL,                       -- sender's user_id
    created_at       timestamptz NOT NULL DEFAULT now(),
    expires_at       timestamptz NOT NULL,
    max_views        integer     NOT NULL DEFAULT 0,             -- 0 = unlimited
    view_count       integer     NOT NULL DEFAULT 0,
    revoked_at       timestamptz,
    session_key_seed bytea       NOT NULL,                       -- HKDF input; per-minute keys derived from this
    PRIMARY KEY (tenant_id, token_id)
);

CREATE INDEX IF NOT EXISTS idx_zt_share_tokens_hash
    ON zt_share_tokens (token_hash);
CREATE INDEX IF NOT EXISTS idx_zt_share_tokens_owner
    ON zt_share_tokens (tenant_id, created_by, created_at DESC);

ALTER TABLE zt_share_tokens ENABLE ROW LEVEL SECURITY;
ALTER TABLE zt_share_tokens FORCE ROW LEVEL SECURITY;
CREATE POLICY zt_share_tokens_tenant_isolation ON zt_share_tokens
    USING (tenant_id::text = current_setting('app.current_tenant', true))
    WITH CHECK (tenant_id::text = current_setting('app.current_tenant', true));

CREATE TABLE IF NOT EXISTS zt_share_telemetry (
    tenant_id   uuid        NOT NULL REFERENCES organizations(id),
    id          uuid        NOT NULL DEFAULT gen_random_uuid(),
    token_id    uuid        NOT NULL,                            -- matches zt_share_tokens.token_id
    event_type  text        NOT NULL,                            -- 'page_view' | 'scroll' | 'focus_blur' | 'devtools_open'
    page_number integer,
    dwell_ms    integer,
    user_agent  text,
    ip_hash     text,                                            -- sha256(ip || tenant_salt) — never raw IP
    created_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id),
    CONSTRAINT zt_share_telemetry_event_type_check
        CHECK (event_type IN ('page_view', 'scroll', 'focus_blur', 'devtools_open'))
);

CREATE INDEX IF NOT EXISTS idx_zt_share_telemetry_token
    ON zt_share_telemetry (tenant_id, token_id, created_at DESC);

ALTER TABLE zt_share_telemetry ENABLE ROW LEVEL SECURITY;
ALTER TABLE zt_share_telemetry FORCE ROW LEVEL SECURITY;
CREATE POLICY zt_share_telemetry_tenant_isolation ON zt_share_telemetry
    USING (tenant_id::text = current_setting('app.current_tenant', true))
    WITH CHECK (tenant_id::text = current_setting('app.current_tenant', true));

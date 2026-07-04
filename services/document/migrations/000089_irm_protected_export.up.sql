-- IRM protected-container export (§5/§8). A "protect & share" seals a document
-- version into an encrypted payload (AES-256-GCM under a fresh payload DEK,
-- stored in S3) plus a signed policy header, and issues one per-recipient
-- license. Opening requires an online license-check callback that validates +
-- logs the open and can be revoked; a revoked license blocks the next open.
--
-- The license-check callback is cross-tenant-by-token (the recipient has no
-- session tenant): the token_hash is a high-entropy secret and is the primary
-- authenticator, mirroring the zero-trust share model (migration 000047). RLS
-- is defense-in-depth; the lookup query resolves tenant_id from the row and all
-- subsequent writes are tenant-scoped.

-- One row per protected export (the sealed container).
CREATE TABLE IF NOT EXISTS irm_containers (
    tenant_id       uuid        NOT NULL REFERENCES organizations(id),
    id              uuid        NOT NULL DEFAULT gen_random_uuid(),
    document_id     uuid        NOT NULL,
    version_id      uuid        NOT NULL,
    sealed_bucket   text        NOT NULL,
    sealed_key      text        NOT NULL,
    payload_nonce   bytea       NOT NULL,           -- AES-GCM nonce for the payload
    payload_sha256  bytea       NOT NULL,           -- sha256 of the ciphertext (integrity)
    mime            text        NOT NULL DEFAULT 'application/octet-stream',
    title           text        NOT NULL DEFAULT '',
    allowed_actions text[]      NOT NULL DEFAULT ARRAY['view'],
    expires_at      timestamptz NOT NULL,
    created_by      uuid        NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    revoked_at      timestamptz,                    -- container-level kill switch (revokes all licenses)
    PRIMARY KEY (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS idx_irm_containers_creator
    ON irm_containers (tenant_id, created_by, created_at DESC);

ALTER TABLE irm_containers ENABLE ROW LEVEL SECURITY;
ALTER TABLE irm_containers FORCE ROW LEVEL SECURITY;
-- The license-check callback resolves a license+container by unique token_hash
-- WITHOUT a request tenant context (the recipient has none). Under a
-- NOBYPASSRLS prod role a strict policy would return 0 rows for that lookup, so
-- the USING clause permits reads ONLY when no tenant context is set (the
-- deliberate token-lookup path, which filters by the secret token_hash); when a
-- tenant context IS set, isolation is enforced normally. WITH CHECK stays strict
-- so a write can never target another tenant.
CREATE POLICY irm_containers_tenant_isolation ON irm_containers
    USING (
        current_setting('app.current_tenant', true) IS NULL
        OR current_setting('app.current_tenant', true) = ''
        OR tenant_id::text = current_setting('app.current_tenant', true)
    )
    WITH CHECK (tenant_id::text = current_setting('app.current_tenant', true));

-- One row per recipient. token_hash is sha256(token) — look up by hash, never
-- store the raw token. wrapped_dek is the payload DEK wrapped under a
-- per-recipient key context (key_ref) so recipients are cryptographically
-- distinct.
CREATE TABLE IF NOT EXISTS irm_licenses (
    tenant_id       uuid        NOT NULL REFERENCES organizations(id),
    id              uuid        NOT NULL DEFAULT gen_random_uuid(),
    container_id    uuid        NOT NULL,
    recipient_type  text        NOT NULL CHECK (recipient_type IN ('user', 'email')),
    recipient_ref   text        NOT NULL,           -- user_id (uuid text) or email
    token_hash      bytea       NOT NULL,
    wrapped_dek     bytea       NOT NULL,
    key_ref         text        NOT NULL,           -- kekID used to wrap (per-recipient context)
    open_count      integer     NOT NULL DEFAULT 0,
    last_opened_at  timestamptz,
    revoked_at      timestamptz,
    revoked_by      uuid,
    created_by      uuid        NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, container_id) REFERENCES irm_containers (tenant_id, id) ON DELETE CASCADE
);
-- token_hash is globally unique (32-byte random token); the cross-tenant
-- callback looks up by hash alone.
CREATE UNIQUE INDEX IF NOT EXISTS idx_irm_licenses_token_hash ON irm_licenses (token_hash);
CREATE INDEX IF NOT EXISTS idx_irm_licenses_container ON irm_licenses (tenant_id, container_id);

ALTER TABLE irm_licenses ENABLE ROW LEVEL SECURITY;
ALTER TABLE irm_licenses FORCE ROW LEVEL SECURITY;
-- Same context-free token-lookup allowance as irm_containers (see above).
CREATE POLICY irm_licenses_tenant_isolation ON irm_licenses
    USING (
        current_setting('app.current_tenant', true) IS NULL
        OR current_setting('app.current_tenant', true) = ''
        OR tenant_id::text = current_setting('app.current_tenant', true)
    )
    WITH CHECK (tenant_id::text = current_setting('app.current_tenant', true));

-- Per-event lifecycle log (issued | opened | revoked | denied). Feeds the audit
-- trail alongside the dms.irm.* outbox events.
CREATE TABLE IF NOT EXISTS irm_license_events (
    tenant_id     uuid        NOT NULL REFERENCES organizations(id),
    id            uuid        NOT NULL DEFAULT gen_random_uuid(),
    container_id  uuid        NOT NULL,
    license_id    uuid,                              -- null for container-level events
    event_type    text        NOT NULL CHECK (event_type IN ('issued', 'opened', 'revoked', 'denied')),
    recipient_ref text        NOT NULL DEFAULT '',
    ip_hash       text        NOT NULL DEFAULT '',   -- sha256(ip || salt); never raw IP
    user_agent    text        NOT NULL DEFAULT '',
    detail        text        NOT NULL DEFAULT '',
    created_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS idx_irm_events_container ON irm_license_events (tenant_id, container_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_irm_events_license ON irm_license_events (tenant_id, license_id, created_at DESC);

ALTER TABLE irm_license_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE irm_license_events FORCE ROW LEVEL SECURITY;
CREATE POLICY irm_license_events_tenant_isolation ON irm_license_events
    USING (tenant_id::text = current_setting('app.current_tenant', true))
    WITH CHECK (tenant_id::text = current_setting('app.current_tenant', true));

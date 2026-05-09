-- ADR 0071 — DocuSign / Adobe Sign third-party connector tables.
--
-- signature_requests already lists 'docusign'/'adobe_sign' as
-- valid providers (initial schema) and has provider_envelope_id —
-- the routing surface is in place. What's missing:
--
--   1. Per-tenant OAuth tokens (one row per (tenant, provider)).
--   2. An idempotent webhook event log keyed on the vendor's event
--      id so redelivery doesn't double-process.

-- ---- 1. esign_oauth_tokens ---------------------------------------
CREATE TABLE IF NOT EXISTS esign_oauth_tokens (
    tenant_id      UUID         NOT NULL REFERENCES organizations(id),
    provider       TEXT         NOT NULL CHECK (provider IN ('docusign','adobe_sign')),
    -- Sealed via pkg/crypto.SealString with the per-tenant KEK so
    -- a Postgres dump alone cannot yield usable bearer tokens.
    access_token   TEXT         NOT NULL,
    refresh_token  TEXT,
    expires_at     TIMESTAMPTZ  NOT NULL,
    -- Vendor-issued discovery values resolved at link time. DocuSign:
    -- accountId + base_uri (regional). Adobe Sign: api_access_point.
    account_id     TEXT,
    base_uri       TEXT,
    scope          TEXT,
    connected_by   UUID,
    connected_at   TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ  NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, provider),
    FOREIGN KEY (tenant_id, connected_by) REFERENCES users(tenant_id, id)
);

CREATE TRIGGER update_esign_oauth_tokens_updated_at
    BEFORE UPDATE ON esign_oauth_tokens FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

ALTER TABLE esign_oauth_tokens ENABLE ROW LEVEL SECURITY;
ALTER TABLE esign_oauth_tokens FORCE  ROW LEVEL SECURITY;
DROP POLICY IF EXISTS esign_oauth_tenant_isolation        ON esign_oauth_tokens;
DROP POLICY IF EXISTS esign_oauth_tenant_isolation_insert ON esign_oauth_tokens;
CREATE POLICY esign_oauth_tenant_isolation ON esign_oauth_tokens
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY esign_oauth_tenant_isolation_insert ON esign_oauth_tokens
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);


-- ---- 2. esign_envelope_events ------------------------------------
-- Append-only log of every webhook the vendor has sent us. The
-- (provider, envelope_id, external_id) triple is the dedup key —
-- both vendors redeliver on transient failures and we never want
-- to double-create a version row.
CREATE TABLE IF NOT EXISTS esign_envelope_events (
    tenant_id     UUID         NOT NULL REFERENCES organizations(id),
    id            UUID         NOT NULL DEFAULT gen_random_uuid(),
    request_id    UUID         NOT NULL,
    provider      TEXT         NOT NULL CHECK (provider IN ('docusign','adobe_sign')),
    envelope_id   TEXT         NOT NULL,
    event_type    TEXT         NOT NULL,
    external_id   TEXT,                            -- vendor's per-event id
    raw_payload   JSONB,
    received_at   TIMESTAMPTZ  NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id),
    -- Dedup target — partial unique because external_id can be NULL
    -- on a few vendor event types (Adobe heartbeat). NULLs don't
    -- collide in Postgres unique indexes by default, so this index
    -- only enforces uniqueness when the vendor gave us an id.
    FOREIGN KEY (tenant_id, request_id) REFERENCES signature_requests(tenant_id, id)
);

-- Hot-path: "did we already see this vendor event?"
CREATE UNIQUE INDEX IF NOT EXISTS idx_esign_events_dedup
    ON esign_envelope_events (provider, envelope_id, external_id)
    WHERE external_id IS NOT NULL;

-- Lookup: "what's the latest event for this envelope?"
CREATE INDEX IF NOT EXISTS idx_esign_events_envelope
    ON esign_envelope_events (tenant_id, envelope_id, received_at DESC);

ALTER TABLE esign_envelope_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE esign_envelope_events FORCE  ROW LEVEL SECURITY;
DROP POLICY IF EXISTS esign_events_tenant_isolation        ON esign_envelope_events;
DROP POLICY IF EXISTS esign_events_tenant_isolation_insert ON esign_envelope_events;
CREATE POLICY esign_events_tenant_isolation ON esign_envelope_events
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY esign_events_tenant_isolation_insert ON esign_envelope_events
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

-- Wave 15.1 — Acknowledgement campaigns (policy attestations).
--
-- Three tables, tenant-scoped, RLS on every one, composite indexes
-- starting with tenant_id. Events are the attestation sub-stream
-- that's chained to services/audit via dms.acknowledgement.* outbox
-- subjects — same hash-chain format as audit_events.

BEGIN;

-- ---------- campaigns ------------------------------------------------
CREATE TABLE IF NOT EXISTS acknowledgement_campaigns (
    tenant_id          UUID NOT NULL,
    id                 UUID NOT NULL DEFAULT gen_random_uuid(),
    document_id        UUID NOT NULL,
    version_id         UUID,
    title              TEXT NOT NULL,
    body_md            TEXT,
    due_at             TIMESTAMPTZ NOT NULL,
    created_by_user_id UUID NOT NULL,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    closed_at          TIMESTAMPTZ,
    status             TEXT NOT NULL DEFAULT 'draft'
                           CHECK (status IN ('draft','active','closed','archived')),
    recipient_policy   JSONB NOT NULL DEFAULT '{}'::jsonb,
    PRIMARY KEY (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS idx_ack_campaigns_tenant_status
    ON acknowledgement_campaigns (tenant_id, status, due_at);
CREATE INDEX IF NOT EXISTS idx_ack_campaigns_tenant_document
    ON acknowledgement_campaigns (tenant_id, document_id);

ALTER TABLE acknowledgement_campaigns ENABLE ROW LEVEL SECURITY;
ALTER TABLE acknowledgement_campaigns FORCE  ROW LEVEL SECURITY;
DO $$ BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_policies WHERE tablename='acknowledgement_campaigns' AND policyname='ack_campaigns_tenant_isolation') THEN
    EXECUTE 'CREATE POLICY ack_campaigns_tenant_isolation ON acknowledgement_campaigns
               USING (tenant_id = current_setting(''app.current_tenant'', true)::uuid)
               WITH CHECK (tenant_id = current_setting(''app.current_tenant'', true)::uuid)';
  END IF;
END $$;
CREATE TRIGGER trg_ack_campaigns_updated_at
    BEFORE UPDATE ON acknowledgement_campaigns FOR EACH ROW
    EXECUTE FUNCTION update_updated_at_column();

-- ---------- assignments ----------------------------------------------
CREATE TABLE IF NOT EXISTS acknowledgement_assignments (
    tenant_id         UUID NOT NULL,
    id                UUID NOT NULL DEFAULT gen_random_uuid(),
    campaign_id       UUID NOT NULL,
    assignee_user_id  UUID NOT NULL,
    assigned_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    reminded_count    INTEGER NOT NULL DEFAULT 0,
    reminded_at       TIMESTAMPTZ,
    acknowledged_at   TIMESTAMPTZ,
    ip_address        INET,
    user_agent        TEXT,
    comment           TEXT,
    attestation_hash  BYTEA,  -- HMAC-SHA256(campaign_id || assignee_user_id || acknowledged_at)
    escalated_at      TIMESTAMPTZ,
    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, campaign_id) REFERENCES acknowledgement_campaigns(tenant_id, id) ON DELETE CASCADE,
    UNIQUE (tenant_id, campaign_id, assignee_user_id)
);
CREATE INDEX IF NOT EXISTS idx_ack_assignments_campaign
    ON acknowledgement_assignments (tenant_id, campaign_id);
CREATE INDEX IF NOT EXISTS idx_ack_assignments_user_pending
    ON acknowledgement_assignments (tenant_id, assignee_user_id)
    WHERE acknowledged_at IS NULL;

ALTER TABLE acknowledgement_assignments ENABLE ROW LEVEL SECURITY;
ALTER TABLE acknowledgement_assignments FORCE  ROW LEVEL SECURITY;
DO $$ BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_policies WHERE tablename='acknowledgement_assignments' AND policyname='ack_assignments_tenant_isolation') THEN
    EXECUTE 'CREATE POLICY ack_assignments_tenant_isolation ON acknowledgement_assignments
               USING (tenant_id = current_setting(''app.current_tenant'', true)::uuid)
               WITH CHECK (tenant_id = current_setting(''app.current_tenant'', true)::uuid)';
  END IF;
END $$;

-- ---------- events ---------------------------------------------------
-- Append-only local audit sub-stream. Mirrors the audit_events hash
-- chain pattern so an export can be verified offline.
CREATE TABLE IF NOT EXISTS acknowledgement_events (
    tenant_id     UUID NOT NULL,
    id            UUID NOT NULL DEFAULT gen_random_uuid(),
    campaign_id   UUID NOT NULL,
    assignment_id UUID,
    actor_user_id UUID,
    event_type    TEXT NOT NULL,
    payload       JSONB NOT NULL DEFAULT '{}'::jsonb,
    prev_hash     BYTEA,
    self_hash     BYTEA NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, campaign_id) REFERENCES acknowledgement_campaigns(tenant_id, id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_ack_events_campaign_time
    ON acknowledgement_events (tenant_id, campaign_id, created_at);

ALTER TABLE acknowledgement_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE acknowledgement_events FORCE  ROW LEVEL SECURITY;
DO $$ BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_policies WHERE tablename='acknowledgement_events' AND policyname='ack_events_tenant_isolation') THEN
    EXECUTE 'CREATE POLICY ack_events_tenant_isolation ON acknowledgement_events
               USING (tenant_id = current_setting(''app.current_tenant'', true)::uuid)
               WITH CHECK (tenant_id = current_setting(''app.current_tenant'', true)::uuid)';
  END IF;
END $$;

-- ---------- signing keys (attestation HMAC, per-tenant) --------------
-- One row per tenant holding a KMS-wrapped 32-byte HMAC signing key.
-- Plaintext never lives on disk; the service unwraps via pkg/crypto
-- on first use per process + caches in-memory.
CREATE TABLE IF NOT EXISTS acknowledgement_signing_keys (
    tenant_id      UUID PRIMARY KEY,
    kek_id         TEXT NOT NULL,
    wrapped_key    BYTEA NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    rotated_at     TIMESTAMPTZ
);
ALTER TABLE acknowledgement_signing_keys ENABLE ROW LEVEL SECURITY;
ALTER TABLE acknowledgement_signing_keys FORCE  ROW LEVEL SECURITY;
DO $$ BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_policies WHERE tablename='acknowledgement_signing_keys' AND policyname='ack_signing_keys_tenant_isolation') THEN
    EXECUTE 'CREATE POLICY ack_signing_keys_tenant_isolation ON acknowledgement_signing_keys
               USING (tenant_id = current_setting(''app.current_tenant'', true)::uuid)
               WITH CHECK (tenant_id = current_setting(''app.current_tenant'', true)::uuid)';
  END IF;
END $$;

-- ---------- outbox (shared with pkg/database.OutboxRepository) -------
CREATE TABLE IF NOT EXISTS outbox (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       UUID NOT NULL,
    event_type      TEXT NOT NULL,
    aggregate_type  TEXT NOT NULL,
    aggregate_id    UUID NOT NULL,
    payload         JSONB NOT NULL,
    published       BOOLEAN NOT NULL DEFAULT false,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    published_at    TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_outbox_unpublished
    ON outbox(created_at) WHERE NOT published;
CREATE INDEX IF NOT EXISTS idx_outbox_tenant_time
    ON outbox(tenant_id, created_at DESC);
ALTER TABLE outbox ENABLE ROW LEVEL SECURITY;
ALTER TABLE outbox FORCE  ROW LEVEL SECURITY;
DO $$ BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_policies WHERE tablename='outbox' AND policyname='outbox_tenant_isolation') THEN
    EXECUTE 'CREATE POLICY outbox_tenant_isolation ON outbox
               USING (tenant_id = current_setting(''app.current_tenant'', true)::uuid)
               WITH CHECK (tenant_id = current_setting(''app.current_tenant'', true)::uuid)';
  END IF;
END $$;

COMMIT;

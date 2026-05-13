-- ADR 0087 — email ingestion (Microsoft Graph + Gmail + IMAP).
--
-- Two tables:
--  * email_ingestion_configs — per-tenant connection + mapping.
--  * email_messages — idempotency + audit trail (source-message-id is
--    unique-per-config so re-polling doesn't double-ingest).

CREATE TABLE IF NOT EXISTS email_ingestion_configs (
    tenant_id                UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    id                       UUID NOT NULL DEFAULT gen_random_uuid(),

    source                   TEXT NOT NULL CHECK (source IN ('microsoft', 'gmail', 'imap')),
    label                    TEXT NOT NULL,
    active                   BOOLEAN NOT NULL DEFAULT TRUE,

    -- OAuth-backed sources point at an existing connector_configs row
    -- (microsoft365 / google_workspace). That row already holds the
    -- encrypted token in its `config` JSONB — we don't re-store it here.
    oauth_provider           TEXT,             -- 'microsoft365' | 'google_workspace' | NULL for imap

    -- IMAP-only fields. Password is encrypted under the per-tenant KEK
    -- via the same convention as services/intelligence::tenant_llm_config.
    imap_host                TEXT,
    imap_port                INTEGER,
    imap_use_tls             BOOLEAN NOT NULL DEFAULT TRUE,
    imap_username            TEXT,
    imap_password_encrypted  BYTEA,

    -- Target placement (v1: one-config-one-folder).
    target_workspace_id      UUID,
    target_folder_id         UUID,

    poll_interval_seconds    INTEGER NOT NULL DEFAULT 300 CHECK (poll_interval_seconds >= 60),

    last_run_at              TIMESTAMPTZ,
    last_error               TEXT,
    last_success_at          TIMESTAMPTZ,
    messages_ingested        BIGINT NOT NULL DEFAULT 0,

    created_by               UUID,
    created_at               TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at               TIMESTAMPTZ NOT NULL DEFAULT now(),

    PRIMARY KEY (tenant_id, id),
    -- IMAP rows must carry host + username; OAuth rows must carry provider.
    -- Enforced loosely (NULL allowed where we don't yet know which it'll be).
    CHECK (
        (source = 'imap' AND imap_host IS NOT NULL AND imap_username IS NOT NULL)
        OR (source IN ('microsoft', 'gmail') AND oauth_provider IS NOT NULL)
    )
);

ALTER TABLE email_ingestion_configs ENABLE ROW LEVEL SECURITY;
ALTER TABLE email_ingestion_configs FORCE ROW LEVEL SECURITY;
CREATE POLICY email_ingestion_configs_tenant_isolation ON email_ingestion_configs
    USING (tenant_id = (current_setting('app.current_tenant', true))::uuid)
    WITH CHECK (tenant_id = (current_setting('app.current_tenant', true))::uuid);

CREATE INDEX IF NOT EXISTS idx_email_configs_active_due
    ON email_ingestion_configs (last_run_at NULLS FIRST)
    WHERE active = TRUE;

GRANT SELECT, INSERT, UPDATE, DELETE ON email_ingestion_configs TO dms_app;


CREATE TABLE IF NOT EXISTS email_messages (
    tenant_id              UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    id                     UUID NOT NULL DEFAULT gen_random_uuid(),
    config_id              UUID NOT NULL,

    source_message_id      TEXT NOT NULL,    -- Graph message id / Gmail id / IMAP UIDVALIDITY+UID
    thread_id              TEXT,
    subject                TEXT,
    sender                 TEXT,
    recipients             JSONB NOT NULL DEFAULT '[]'::jsonb,
    received_at            TIMESTAMPTZ,

    -- Document the body materialised into; NULL while pending or after
    -- a transient failure that retried but hasn't yet succeeded.
    document_id            UUID,
    attachment_document_ids UUID[] NOT NULL DEFAULT '{}',

    ingest_status          TEXT NOT NULL DEFAULT 'pending'
                                  CHECK (ingest_status IN ('pending', 'materialised', 'failed')),
    ingest_error           TEXT,

    ingested_at            TIMESTAMPTZ NOT NULL DEFAULT now(),

    PRIMARY KEY (tenant_id, id),
    -- Idempotency: the same source message can't be ingested twice
    -- through the same config.
    UNIQUE (tenant_id, config_id, source_message_id),

    FOREIGN KEY (tenant_id, config_id)
        REFERENCES email_ingestion_configs(tenant_id, id) ON DELETE CASCADE
);

ALTER TABLE email_messages ENABLE ROW LEVEL SECURITY;
ALTER TABLE email_messages FORCE ROW LEVEL SECURITY;
CREATE POLICY email_messages_tenant_isolation ON email_messages
    USING (tenant_id = (current_setting('app.current_tenant', true))::uuid)
    WITH CHECK (tenant_id = (current_setting('app.current_tenant', true))::uuid);

CREATE INDEX IF NOT EXISTS idx_email_messages_config_received
    ON email_messages (tenant_id, config_id, received_at DESC);
CREATE INDEX IF NOT EXISTS idx_email_messages_pending
    ON email_messages (tenant_id, config_id)
    WHERE ingest_status = 'pending';

GRANT SELECT, INSERT, UPDATE, DELETE ON email_messages TO dms_app;

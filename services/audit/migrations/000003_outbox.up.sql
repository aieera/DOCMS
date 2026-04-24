-- Wave 17 / audit tamper-detection emit.
--
-- Audit was consumer-only pre-Wave-17; tamper_detected.v1 emission
-- requires a local outbox so the publish is tx-atomic with the
-- verify result write. Shape matches the document service's outbox
-- (services/document/migrations/000001_initial_schema.up.sql:1279)
-- so pkg/database.OutboxRepository + NewOutboxPublisher work
-- unchanged.
BEGIN;

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

CREATE POLICY outbox_tenant_isolation ON outbox
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);

COMMIT;

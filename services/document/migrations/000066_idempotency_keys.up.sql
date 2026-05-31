-- 000066 — Idempotency keys for ERP-integration writes.
--
-- Backs the "one DMS document per ERP record" guarantee that the ERP's
-- dms_sync_log enforces via UNIQUE(entity_type, entity_id). A retried
-- POST /api/v1/documents or POST /api/v1/documents/{id}/versions carrying the
-- same Idempotency-Key replays the original stored response instead of
-- creating a duplicate. Concurrent in-flight retries see status='in_progress'
-- and get 409 so they back off.
--
-- Tenant-scoped + RLS like every other tenant table; the app role is
-- NOBYPASSRLS (see 000065) so a query that forgets the tenant predicate
-- fails closed.
CREATE TABLE idempotency_keys (
    tenant_id        UUID        NOT NULL REFERENCES organizations(id),
    idempotency_key  TEXT        NOT NULL,
    request_method   TEXT        NOT NULL,
    request_path     TEXT        NOT NULL,
    status           TEXT        NOT NULL DEFAULT 'in_progress', -- in_progress | completed
    response_status  INT         NULL,
    response_body    BYTEA       NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at     TIMESTAMPTZ NULL,
    PRIMARY KEY (tenant_id, idempotency_key)
);

ALTER TABLE idempotency_keys ENABLE ROW LEVEL SECURITY;
CREATE POLICY idempotency_keys_tenant_isolation ON idempotency_keys
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY idempotency_keys_tenant_isolation_insert ON idempotency_keys
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

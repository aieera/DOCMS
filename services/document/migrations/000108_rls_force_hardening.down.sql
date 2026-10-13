DROP POLICY IF EXISTS privacy_ledger_tenant_isolation_insert ON privacy_ledger;
DROP POLICY IF EXISTS privacy_ledger_tenant_isolation        ON privacy_ledger;
ALTER TABLE privacy_ledger NO FORCE ROW LEVEL SECURITY;
ALTER TABLE privacy_ledger DISABLE  ROW LEVEL SECURITY;

DROP POLICY IF EXISTS outbox_dlq_tenant_isolation_insert ON outbox_dlq;
DROP POLICY IF EXISTS outbox_dlq_tenant_isolation        ON outbox_dlq;
ALTER TABLE outbox_dlq NO FORCE ROW LEVEL SECURITY;
ALTER TABLE outbox_dlq DISABLE  ROW LEVEL SECURITY;

ALTER TABLE document_entities NO FORCE ROW LEVEL SECURITY;
ALTER TABLE idempotency_keys  NO FORCE ROW LEVEL SECURITY;
ALTER TABLE tenant_keks       NO FORCE ROW LEVEL SECURITY;

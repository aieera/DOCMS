-- Review O-7 / L-10 / M-28 — every tenant table is ENABLE + FORCE row
-- level security.
--
-- ENABLE without FORCE leaves the table OWNER unfiltered, and Helm and
-- Ansible ran the services as the owner, so these tables had no tenant
-- isolation there (O-7, L-10): tenant_keks (000004), idempotency_keys
-- (000066), document_entities (000021). outbox_dlq (000094) and
-- privacy_ledger (000006) had no RLS at all (M-28).
--
-- Every runtime accessor already sets app.current_tenant:
--   tenant_keks       — storage liveKEKAlias, auth encryption admin
--                       (WithTenantTx); auth tenant_admin and dms-admin kms
--                       must do the same when run as the owner.
--   idempotency_keys  — pkg/middleware.Idempotency (WithTenantTx).
--   document_entities — document NER repo, intelligence persist.py.
--   outbox_dlq        — the outbox drain sets each row's tenant before the
--                       dead-letter insert (000104 / DEC-6); its metrics
--                       count through outbox_drain_stats(); `dms-admin
--                       outbox replay-dlq` runs per tenant.
--   privacy_ledger    — workflow WritePrivacyLedger (WithTenantTx). 000006
--                       left it unscoped so a compliance officer could read
--                       across tenants; no such reader exists, and one must
--                       now go through a SECURITY DEFINER function (DEC-2).
--
-- The intelligence-owned ledgers get FORCE in intelligence 000006.

ALTER TABLE tenant_keks       FORCE ROW LEVEL SECURITY;
ALTER TABLE idempotency_keys  FORCE ROW LEVEL SECURITY;
ALTER TABLE document_entities FORCE ROW LEVEL SECURITY;

ALTER TABLE outbox_dlq ENABLE ROW LEVEL SECURITY;
ALTER TABLE outbox_dlq FORCE  ROW LEVEL SECURITY;
CREATE POLICY outbox_dlq_tenant_isolation ON outbox_dlq
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY outbox_dlq_tenant_isolation_insert ON outbox_dlq
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

ALTER TABLE privacy_ledger ENABLE ROW LEVEL SECURITY;
ALTER TABLE privacy_ledger FORCE  ROW LEVEL SECURITY;
CREATE POLICY privacy_ledger_tenant_isolation ON privacy_ledger
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY privacy_ledger_tenant_isolation_insert ON privacy_ledger
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

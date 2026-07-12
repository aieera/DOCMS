-- Idempotency for ProvisionFromTemplate (ADR 0118, audit gap note).
--
-- A retried provision (double-click, client retry after a timeout) would
-- otherwise scaffold a DUPLICATE folder/doc tree. With a client-supplied
-- idempotency key the server stores the first provision's result keyed on
-- (tenant, key) and replays it on retry instead of re-provisioning. The
-- input_digest lets an accidental key reuse with a DIFFERENT request be
-- rejected rather than silently replaying an unrelated result.
--
-- The row is written in the SAME transaction as the provisioned tree, so the
-- unique (tenant_id, idempotency_key) key makes "provision the tree AND claim
-- the key" atomic: a concurrent duplicate loses the INSERT race, rolls back
-- its tree, and replays the winner's result. FORCE RLS keeps it tenant-scoped
-- like every other tenant table.
CREATE TABLE template_provision_requests (
    tenant_id       UUID         NOT NULL REFERENCES organizations(id),
    idempotency_key TEXT         NOT NULL,
    input_digest    TEXT         NOT NULL, -- sha256 of the canonicalized ProvisionInput
    result          JSONB        NOT NULL, -- the ProvisionResult replayed on retry
    created_at      TIMESTAMPTZ  NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, idempotency_key)
);
ALTER TABLE template_provision_requests ENABLE ROW LEVEL SECURITY;
ALTER TABLE template_provision_requests FORCE  ROW LEVEL SECURITY;
CREATE POLICY tpr_tenant_isolation ON template_provision_requests
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY tpr_tenant_isolation_insert ON template_provision_requests
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

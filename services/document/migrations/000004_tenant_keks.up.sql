-- Wave 6 Prompt 6.1: per-tenant KEK registry.
--
-- Replaces the single-shared `vaultdms-storage-default` KEK with a
-- per-tenant keyed lookup. See ADR 0026 for the derivation strategy
-- (dev/on-prem: HKDF from master secret; prod: Vault/AWS KMS alias).
--
-- tenant_keks has one or more rows per tenant — the current live row
-- is `retired_at IS NULL`. Rotation inserts a new row with version+1
-- and updates the retired_at of the previous active row. Existing
-- blobs encrypted under the retired KEK stay readable because the
-- row stays in the table; only new encrypts use the live version.
--
-- Index plan:
--   PK (tenant_id, version) is the hot lookup path at write time.
--   Unique (tenant_id) WHERE retired_at IS NULL means exactly one
--   live KEK per tenant — enforced by DB, not application logic.
BEGIN;

CREATE TABLE tenant_keks (
    tenant_id   UUID        NOT NULL REFERENCES organizations(id),
    version     INT         NOT NULL CHECK (version > 0),
    alias       TEXT        NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    retired_at  TIMESTAMPTZ,
    retired_by  UUID,
    PRIMARY KEY (tenant_id, version)
);

CREATE UNIQUE INDEX tenant_keks_one_live_per_tenant
    ON tenant_keks (tenant_id)
    WHERE retired_at IS NULL;

ALTER TABLE tenant_keks ENABLE ROW LEVEL SECURITY;

CREATE POLICY tenant_keks_tenant_isolation
    ON tenant_keks
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);

-- Seed a v1 row for every existing tenant so pre-existing blobs have
-- a lookup target. Alias uses the canonical "vaultdms/tenant/<uuid>"
-- form that both LocalKeyManager and KMS providers understand.
INSERT INTO tenant_keks (tenant_id, version, alias, created_at)
SELECT id, 1, 'vaultdms/tenant/' || id::text, now()
  FROM organizations
 WHERE NOT EXISTS (
     SELECT 1 FROM tenant_keks WHERE tenant_keks.tenant_id = organizations.id
 );

COMMIT;

-- Ed25519-signed tamper-evidence checkpoints over the audit hash chain.
--
-- The existing SHA-256 chain (audit_events.previous_hash/event_hash) +
-- /verify-integrity prove the chain is internally consistent, but an
-- attacker with DB write access could recompute every hash and forge a
-- self-consistent chain. A checkpoint signs the chain head (head_hash +
-- event_count) with a private key held OUTSIDE the database, so any
-- party with the public key can independently detect tampering of the
-- first N events — the forger can't produce a valid signature.
--
-- Append-only (UPDATE/DELETE revoked) and tenant-isolated via RLS, the
-- same posture as audit_events. Writes/reads go through
-- database.WithTenantTx so the app.current_tenant GUC is set.

CREATE TABLE IF NOT EXISTS audit_checkpoints (
    tenant_id   UUID        NOT NULL,
    id          UUID        NOT NULL DEFAULT gen_random_uuid(),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    head_hash   TEXT        NOT NULL,        -- event_hash of the event at position event_count
    event_count BIGINT      NOT NULL,        -- number of events covered by this checkpoint
    algo        TEXT        NOT NULL,        -- signature algorithm, e.g. 'ed25519'
    key_id      TEXT        NOT NULL,        -- fingerprint of the signing public key
    signature   TEXT        NOT NULL,        -- base64 signature over the canonical message
    PRIMARY KEY (tenant_id, id)
);

CREATE INDEX IF NOT EXISTS idx_audit_checkpoints_tenant_time
    ON audit_checkpoints (tenant_id, created_at DESC, id DESC);

ALTER TABLE audit_checkpoints ENABLE ROW LEVEL SECURITY;
ALTER TABLE audit_checkpoints FORCE  ROW LEVEL SECURITY;

DROP POLICY IF EXISTS audit_checkpoints_tenant_isolation ON audit_checkpoints;
CREATE POLICY audit_checkpoints_tenant_isolation ON audit_checkpoints
    FOR ALL
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid)
    WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

-- Append-only: a checkpoint must never be mutated or removed.
REVOKE UPDATE, DELETE ON audit_checkpoints FROM PUBLIC;

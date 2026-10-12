-- Server-side, single-use, user-bound OAuth state for native connectors
-- (Google, Microsoft 365).
--
-- The connector OAuth `state` was a stateless HMAC over
-- tenant|connector|nonce|exp: unpredictable and expiring, but replayable
-- for its whole 10-minute window. Each authorize now records its nonce
-- here with the PKCE code_verifier and the user who started it
-- (created_by). The callback consumes the row (DELETE ... RETURNING) only
-- when it arrives on that same user's session, then sends the verifier
-- with the code exchange. A replayed state finds no row; a callback by
-- another user or without a session is refused and does NOT consume the
-- row (connect-account CSRF: a third party holding the authorize URL can
-- neither bind their own account nor burn the starter's flow). PKCE alone
-- would not stop that, since the server holds the verifier.
--
-- Tenant table: tenant_id leads the PK and FORCE RLS applies like every
-- other connector table. The callback learns the tenant from the
-- HMAC-verified state, so the consume runs inside WithTenantTx.

BEGIN;

CREATE TABLE IF NOT EXISTS connector_oauth_states (
    tenant_id      UUID        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    nonce          TEXT        NOT NULL,
    connector_type TEXT        NOT NULL,
    code_verifier  TEXT        NOT NULL,
    created_by     UUID,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at     TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (tenant_id, nonce)
);

CREATE INDEX IF NOT EXISTS idx_connector_oauth_states_expires
    ON connector_oauth_states (tenant_id, expires_at);

ALTER TABLE connector_oauth_states ENABLE ROW LEVEL SECURITY;
ALTER TABLE connector_oauth_states FORCE  ROW LEVEL SECURITY;

DROP POLICY IF EXISTS connector_oauth_states_tenant_isolation ON connector_oauth_states;
CREATE POLICY connector_oauth_states_tenant_isolation ON connector_oauth_states
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid)
    WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'dms_app') THEN
        GRANT SELECT, INSERT, DELETE ON connector_oauth_states TO dms_app;
    END IF;
END $$;

-- Global host_path uniqueness for watched-folder intake (N-6, defence in
-- depth). Per-tenant confinement to <SEDOC_INTAKE_ROOT>/<tenant_id> already
-- makes cross-tenant overlap impossible; this index makes an identical path
-- impossible at the database too. intake_drop_folders belongs to document
-- migration 000042, so the index is created only when that table exists.
-- If existing rows already share a host_path across tenants the index
-- cannot be built: the migration logs a WARNING and continues instead of
-- failing the deploy (the application checks still apply); resolve the
-- duplicates and create the index by hand. The exception handler is used
-- rather than a pre-check because FORCE RLS hides other tenants' rows from
-- a pre-check run as the owner role, while the index build sees them all.
DO $$
BEGIN
    IF to_regclass('public.intake_drop_folders') IS NULL THEN
        RAISE NOTICE 'intake_drop_folders absent; global host_path unique index skipped';
    ELSE
        BEGIN
            CREATE UNIQUE INDEX IF NOT EXISTS uq_intake_drop_folders_host_path
                ON intake_drop_folders (host_path);
        EXCEPTION WHEN unique_violation THEN
            RAISE WARNING 'intake_drop_folders has host_path values shared across tenants; uq_intake_drop_folders_host_path NOT created';
        END;
    END IF;
END $$;

COMMIT;

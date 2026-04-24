-- Wave 15.3 / force-change-password.
--
-- Adds the password lifecycle columns required to (a) flag users who
-- must change their password before next session issuance, (b) track
-- last N passwords to reject reuse, (c) mark SSO-federated users so
-- expiry cron + change-password flows skip them.
--
-- Wave 12.5 per-service migration split is deferred (see
-- docs/backlog/out-of-scope.md); `users` still lives in the document
-- service's bulk schema, so this migration uses IF NOT EXISTS and
-- targets columns additively. No data migration other than the
-- backfill of password_changed_at = created_at for existing rows.

BEGIN;

ALTER TABLE users
    ADD COLUMN IF NOT EXISTS password_changed_at   TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS must_change_password  BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS password_expires_at   TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS sso_federated         BOOLEAN NOT NULL DEFAULT false;

-- Backfill password_changed_at for existing rows so the expiry cron
-- has a sane origin. NULL-guarded so re-running the migration is a
-- no-op.
UPDATE users
   SET password_changed_at = created_at
 WHERE password_changed_at IS NULL
   AND password_hash IS NOT NULL;

-- Mark rows with NULL password_hash as SSO-federated. Local accounts
-- without a hash shouldn't exist post-Wave-5 but the guard is cheap.
UPDATE users
   SET sso_federated = true
 WHERE password_hash IS NULL;

-- Index used by the expiry sweeper. Partial to keep it small: the
-- sweeper only cares about rows that actually have an expiry set and
-- haven't already been flagged.
CREATE INDEX IF NOT EXISTS idx_users_password_expires_pending
    ON users (tenant_id, password_expires_at)
 WHERE password_expires_at IS NOT NULL
   AND must_change_password = false
   AND sso_federated = false
   AND deleted_at IS NULL;

-- --------------------------------------------------------------------
-- password_history — last N bcrypt hashes per user so we can reject
-- reuse. N is enforced in the service layer (default 5); we trim on
-- insert.
-- --------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS password_history (
    tenant_id    UUID NOT NULL,
    user_id      UUID NOT NULL,
    id           UUID NOT NULL DEFAULT gen_random_uuid(),
    password_hash TEXT NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, user_id, id),
    FOREIGN KEY (tenant_id, user_id) REFERENCES users(tenant_id, id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_password_history_recent
    ON password_history (tenant_id, user_id, created_at DESC);

ALTER TABLE password_history ENABLE ROW LEVEL SECURITY;
ALTER TABLE password_history FORCE  ROW LEVEL SECURITY;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_policies
         WHERE schemaname = current_schema()
           AND tablename  = 'password_history'
           AND policyname = 'password_history_tenant_isolation'
    ) THEN
        EXECUTE 'CREATE POLICY password_history_tenant_isolation ON password_history
                   USING (tenant_id = current_setting(''app.current_tenant'', true)::uuid)
                   WITH CHECK (tenant_id = current_setting(''app.current_tenant'', true)::uuid)';
    END IF;
END $$;

-- --------------------------------------------------------------------
-- Tenant-level password policy. Additive column on organizations; 0
-- means "never expire". Defaults to 90 days per the Wave 15.3 brief.
-- --------------------------------------------------------------------
ALTER TABLE organizations
    ADD COLUMN IF NOT EXISTS password_expiry_days INTEGER NOT NULL DEFAULT 90
        CHECK (password_expiry_days >= 0 AND password_expiry_days <= 3650);

COMMIT;

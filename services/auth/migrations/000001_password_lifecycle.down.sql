BEGIN;

DROP INDEX IF EXISTS idx_users_password_expires_pending;

ALTER TABLE users
    DROP COLUMN IF EXISTS password_changed_at,
    DROP COLUMN IF EXISTS must_change_password,
    DROP COLUMN IF EXISTS password_expires_at,
    DROP COLUMN IF EXISTS sso_federated;

DROP TABLE IF EXISTS password_history;

ALTER TABLE organizations
    DROP COLUMN IF EXISTS password_expiry_days;

COMMIT;

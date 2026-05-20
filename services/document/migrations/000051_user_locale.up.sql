-- ADR 0106 — i18n foundation (react-i18next).
--
-- The `locale` column was added in the original users-table migration
-- but as a nullable TEXT with no value constraint. Now that the
-- frontend ships i18n (en + ar), tighten it:
--   * NOT NULL with 'en' default (already the value in practice)
--   * CHECK constraint so we don't accept arbitrary BCP-47 tags before
--     the application is ready to serve them (every locale we accept
--     must have a translation namespace bundle on disk)
--   * Index for cohort queries (e.g. "how many Arabic users do we
--     have, do we need to prioritise more namespaces")
--
-- Note: this migration lives in the document service even though the
-- column gates auth-service behaviour, because the users table itself
-- is owned by the document service (per
-- 000001_initial_schema.up.sql). Cross-service column ownership is a
-- known wart called out in the architecture docs.

-- Step 1: backfill any nulls before tightening the column. Safe even
-- if no rows are NULL (no-op for fresh deploys).
UPDATE users SET locale = 'en' WHERE locale IS NULL OR locale = '';

-- Step 2: enforce NOT NULL.
ALTER TABLE users ALTER COLUMN locale SET NOT NULL;
ALTER TABLE users ALTER COLUMN locale SET DEFAULT 'en';

-- Step 3: lock down the accepted set. Expand the IN-list when a new
-- locale's namespace bundle ships in web/public/locales/<code>/.
ALTER TABLE users
  ADD CONSTRAINT users_locale_check CHECK (locale IN ('en', 'ar'));

-- Step 4: cohort index. WHERE deleted_at IS NULL matches the other
-- user indexes in this schema.
CREATE INDEX IF NOT EXISTS idx_users_locale
  ON users (tenant_id, locale)
  WHERE deleted_at IS NULL;

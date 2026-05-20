-- Reverse 000051_user_locale.up.sql.
DROP INDEX IF EXISTS idx_users_locale;
ALTER TABLE users DROP CONSTRAINT IF EXISTS users_locale_check;
ALTER TABLE users ALTER COLUMN locale DROP NOT NULL;
ALTER TABLE users ALTER COLUMN locale SET DEFAULT 'en';

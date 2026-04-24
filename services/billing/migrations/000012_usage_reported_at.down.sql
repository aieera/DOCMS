BEGIN;
DROP INDEX IF EXISTS idx_usage_records_unreported;
ALTER TABLE usage_records DROP COLUMN IF EXISTS reported_at;
COMMIT;

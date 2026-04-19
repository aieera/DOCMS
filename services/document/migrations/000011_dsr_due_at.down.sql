BEGIN;
DROP INDEX IF EXISTS idx_dsr_pending_due;
ALTER TABLE privacy_dsr_requests DROP COLUMN IF EXISTS due_at;
COMMIT;

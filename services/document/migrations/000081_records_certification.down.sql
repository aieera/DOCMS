DROP INDEX IF EXISTS idx_records_frozen;
DROP INDEX IF EXISTS idx_records_vital;
ALTER TABLE records
    DROP COLUMN IF EXISTS vital_record,
    DROP COLUMN IF EXISTS frozen,
    DROP COLUMN IF EXISTS frozen_by,
    DROP COLUMN IF EXISTS frozen_at,
    DROP COLUMN IF EXISTS freeze_reason,
    DROP COLUMN IF EXISTS metadata;

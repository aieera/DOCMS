ALTER TABLE legal_holds DROP CONSTRAINT IF EXISTS legal_holds_origin_check;
ALTER TABLE legal_holds DROP COLUMN IF EXISTS origin;

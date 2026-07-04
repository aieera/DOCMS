ALTER TABLE ocr_results
    DROP COLUMN IF EXISTS corrected_text,
    DROP COLUMN IF EXISTS corrected_by,
    DROP COLUMN IF EXISTS corrected_at;

-- Manual OCR correction. When a reviewer fixes the recognised text for a
-- page (the correction field in the OCR review UI), we keep the original
-- engine output in text_content untouched and store the human-corrected
-- version alongside it. Downstream consumers (search reindex, extraction)
-- prefer corrected_text when present, falling back to text_content.
--
-- Nullable: the vast majority of pages are never hand-corrected, so this
-- adds no write cost to the OCR worker's insert path.
ALTER TABLE ocr_results
    ADD COLUMN IF NOT EXISTS corrected_text TEXT,
    ADD COLUMN IF NOT EXISTS corrected_by   UUID,
    ADD COLUMN IF NOT EXISTS corrected_at   TIMESTAMPTZ;

BEGIN;
DROP POLICY IF EXISTS ocr_processed_events_tenant_isolation ON ocr_processed_events;
DROP TABLE IF EXISTS ocr_processed_events;
COMMIT;

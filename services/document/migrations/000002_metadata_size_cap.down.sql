BEGIN;
ALTER TABLE documents DROP CONSTRAINT IF EXISTS documents_custom_metadata_size_check;
COMMIT;

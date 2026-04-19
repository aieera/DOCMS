-- Defense-in-depth against oversize custom_metadata payloads. The service
-- layer rejects >64 KiB at the API boundary; this check catches anything
-- that slips past (direct DB writes, mis-configured services, replication).

BEGIN;

ALTER TABLE documents
    ADD CONSTRAINT documents_custom_metadata_size_check
    CHECK (pg_column_size(custom_metadata) < 65536);

COMMIT;

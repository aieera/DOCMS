-- D-25 — undo 000114: remove only the '_sedoc.source' key the backfill set
-- (value "m365" on rows that also carry 'email.source'); every other
-- metadata key is left as it is. Same bounded batching as the up migration.
DO $$
DECLARE
    n integer;
BEGIN
    LOOP
        UPDATE documents
           SET custom_metadata = custom_metadata - '_sedoc.source'
         WHERE ctid IN (
                 SELECT ctid
                   FROM documents
                  WHERE custom_metadata ? 'email.source'
                    AND custom_metadata->>'_sedoc.source' = 'm365'
                  LIMIT 5000);
        GET DIAGNOSTICS n = ROW_COUNT;
        EXIT WHEN n = 0;
    END LOOP;
END $$;

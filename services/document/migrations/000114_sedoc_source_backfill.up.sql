-- D-25 — backfill the reserved _sedoc.source marker on legacy m365 ingests.
--
-- The orphan GC used to exempt version-less documents carrying the
-- user-writable custom_metadata key 'email.source'; it now exempts only the
-- reserved '_sedoc.source' key, which no user-facing metadata write can set
-- and which the m365 ingest writes from this release on. Rows ingested
-- before that (metadata-only, filed while the storage pipeline was not
-- wired) carry only 'email.source' and would be reaped 24 h after deploy.
-- This one-off backfill gives them the marker.
--
-- Data only: no index, no constraint. The UPDATE runs in bounded batches
-- (5000 rows per statement, selected by ctid) so no single statement scans
-- and rewrites the whole hot table; it is idempotent (already-marked rows
-- are skipped), so a re-run is a no-op. Row locks are taken only on the
-- rows being marked. Like every data backfill in this chain, it assumes the
-- migration role bypasses RLS (superuser / table owner with BYPASSRLS).
DO $$
DECLARE
    n integer;
BEGIN
    LOOP
        UPDATE documents
           SET custom_metadata = custom_metadata || '{"_sedoc.source": "m365"}'::jsonb
         WHERE ctid IN (
                 SELECT ctid
                   FROM documents
                  WHERE custom_metadata ? 'email.source'
                    AND NOT (custom_metadata ? '_sedoc.source')
                  LIMIT 5000);
        GET DIAGNOSTICS n = ROW_COUNT;
        EXIT WHEN n = 0;
    END LOOP;
END $$;

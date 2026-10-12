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
-- Only genuine ingests are marked: 'email.source' must hold the exact value
-- the Outlook ingest has always written, 'm365_outlook_addin'
-- (handler/m365_ingest.go parentMetadata/childMetadata). A user-set
-- 'email.source' with any other value is not an ingest and stays unmarked.
--
-- Data only: no index, no constraint. Each UPDATE marks at most 5000 rows
-- (selected by ctid), which bounds the size of every statement — it does
-- NOT bound lock time: golang-migrate applies this file as one transaction,
-- so the row locks on every marked row are held until the migration
-- commits. Only rows matching the predicate are locked; there is no table
-- lock beyond ROW EXCLUSIVE. Idempotent: already-marked rows are skipped,
-- so a re-run is a no-op. Like every data backfill in this chain, it
-- assumes the migration role bypasses RLS (superuser / table owner with
-- BYPASSRLS).
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
                  WHERE custom_metadata->>'email.source' = 'm365_outlook_addin'
                    AND NOT (custom_metadata ? '_sedoc.source')
                  LIMIT 5000);
        GET DIAGNOSTICS n = ROW_COUNT;
        EXIT WHEN n = 0;
    END LOOP;
END $$;

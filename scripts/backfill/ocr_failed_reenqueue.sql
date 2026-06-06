-- OCR-4 backfill — re-enqueue every historical failed OCR event
-- so the worker (now carrying the c4eadca NaN scrub + 6d124be decrypt +
-- IndexError guards) gets a fresh chance at the bytes.
--
-- Mechanism: insert one new outbox row per failed (tenant_id, version_id)
-- with the same VersionUploadedPayload shape services/document/internal/
-- handler/ocr_handler.go uses for /ocr/rerun. The outbox publisher
-- forwards to NATS, the intelligence nats_consumer enqueues a Celery
-- job, the worker processes — same path as a real upload.
--
-- Idempotency: ocr_processed_events PK is (tenant_id, event_id). We
-- mint a fresh event_id per row so dedupe doesn't suppress the rerun.
-- The OLD failed row stays in the ledger as historical evidence.
--
-- RLS: outbox is tenant-scoped FORCE RLS. Loop per-tenant and SET LOCAL
-- app.current_tenant inside the tx so each insert passes the policy.
--
-- Usage:
--   docker exec -i sedoc-postgres psql -U sedoc -d sedoc \
--     < scripts/backfill/ocr_failed_reenqueue.sql
--
-- To re-run only a specific subset (e.g. just encrypted blob failures),
-- edit the WHERE clause in the source CTE before running.

\timing on
\echo
\echo '=== OCR-4 backfill — failed-event re-enqueue ==='
\echo

-- Show what we'll touch (for the audit log)
SELECT
  CASE WHEN length(b.encrypted_dek) > 0 THEN 'encrypted' ELSE 'plaintext' END AS blob_state,
  COUNT(*) AS will_reenqueue
FROM ocr_processed_events ope
JOIN document_versions v ON v.tenant_id = ope.tenant_id AND v.id = ope.version_id
JOIN content_blobs b ON b.tenant_id = v.tenant_id AND b.id = v.content_blob_id
WHERE ope.status = 'failed'
GROUP BY 1
ORDER BY 1;

\echo

DO $$
DECLARE
  rec       RECORD;
  ev_id     UUID;
  payload   JSONB;
  reenq     INTEGER := 0;
BEGIN
  -- One transaction per tenant to keep app.current_tenant scoped.
  FOR rec IN
    SELECT DISTINCT tenant_id FROM ocr_processed_events WHERE status = 'failed'
  LOOP
    PERFORM set_config('app.current_tenant', rec.tenant_id::text, true);

    -- All failed events for this tenant, with the version + blob fields
    -- the consumer needs to actually dispatch the job.
    FOR ev_id, payload IN
      SELECT
        gen_random_uuid(),
        jsonb_build_object(
          'event_id',           gen_random_uuid()::text,
          'tenant_id',          ope.tenant_id::text,
          'document_id',        ope.document_id::text,
          'version_id',         ope.version_id::text,
          'version_number',     v.version_number,
          'content_blob_id',    v.content_blob_id::text,
          'storage_uri',        's3://' || b.storage_bucket || '/' || b.storage_key,
          'mime_type',          COALESCE(v.mime_type, ''),
          'size_bytes',         v.size_bytes,
          'sha256',             COALESCE(v.sha256_hash, ''),
          'uploaded_by_user_id', '',
          'uploaded_at',        to_char(now() AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
          'reason',             'backfill_ocr4'
        )
      FROM ocr_processed_events ope
      JOIN document_versions v
        ON v.tenant_id = ope.tenant_id AND v.id = ope.version_id
      JOIN content_blobs b
        ON b.tenant_id = v.tenant_id AND b.id = v.content_blob_id
      WHERE ope.tenant_id = rec.tenant_id
        AND ope.status   = 'failed'
    LOOP
      INSERT INTO outbox (id, tenant_id, event_type, aggregate_type, aggregate_id, payload)
      VALUES (ev_id,
              rec.tenant_id,
              'dms.version.uploaded.v1',
              'version',
              (payload->>'version_id')::uuid,
              payload);
      reenq := reenq + 1;
    END LOOP;
  END LOOP;
  RAISE NOTICE 'OCR-4 backfill: enqueued % rerun events', reenq;
END $$;

\echo
\echo '=== outbox queue state after backfill ==='
SELECT event_type, COUNT(*) AS pending
  FROM outbox
 WHERE published = false
 GROUP BY event_type
 ORDER BY pending DESC LIMIT 5;

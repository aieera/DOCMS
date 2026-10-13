-- DEC-6 / review C-4 — let the shared outbox drain work under the
-- NOBYPASSRLS runtime role (dms_app).
--
-- outbox is FORCE ROW LEVEL SECURITY, keyed on app.current_tenant. The
-- OutboxPublisher drains every tenant's rows from one process, so it
-- cannot set a tenant before it knows which rows are due: as dms_app the
-- old raw SELECT saw zero rows forever while inserts kept succeeding (no
-- search indexing, audit, notifications, webhooks or AI post-processing).
-- 000094 deferred this to issue #88.
--
-- Same shape as the pre-tenant lookups in
-- services/auth/migrations/000001_pretenant_lookup_functions.up.sql
-- (DEC-2): a SECURITY DEFINER function owned by a dedicated NOLOGIN
-- BYPASSRLS role, search_path pinned, EXECUTE revoked from PUBLIC and
-- granted to dms_app only.
--
--   outbox_claim_batch(p_limit) returns the due, unpublished rows
--     ORDER BY created_at, id ... FOR UPDATE SKIP LOCKED. The row locks
--     belong to the CALLER's transaction, so concurrent publishers still
--     partition the work. Everything after the claim (mark published,
--     record a retry, move to outbox_dlq) runs as dms_app with
--     app.current_tenant set to each row's tenant, so RLS still checks
--     every write.
--   outbox_drain_stats() returns only two aggregates for the drain's
--     metrics (DLQ depth, age of the oldest unpublished row — backoff-
--     deferred rows included), which a tenant-scoped read cannot compute.
--
-- Review H-13: a transient publish failure (NATS unreachable or timing
-- out) does not count toward the dead-letter attempts. transient_failures
-- counts consecutive transient failures so their retry delay can back off
-- per row (BaseBackoff * 2^(n-1), capped) instead of by the row's age.
--
-- Rows queued before this migration keep their shape (000094 added
-- attempts/next_attempt_at with defaults; transient_failures defaults to
-- 0), so they drain unchanged.

ALTER TABLE outbox
    ADD COLUMN IF NOT EXISTS transient_failures INT NOT NULL DEFAULT 0;

DO $$
BEGIN
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'dms_outbox_drain') THEN
    CREATE ROLE dms_outbox_drain NOLOGIN BYPASSRLS;
  ELSE
    ALTER ROLE dms_outbox_drain NOLOGIN BYPASSRLS;
  END IF;
END $$;

-- BYPASSRLS skips policies, not grants. FOR UPDATE needs UPDATE as well
-- as SELECT; the stats function only counts outbox_dlq.
GRANT SELECT, UPDATE ON outbox     TO dms_outbox_drain;
GRANT SELECT         ON outbox_dlq TO dms_outbox_drain;

CREATE OR REPLACE FUNCTION outbox_claim_batch(p_limit int)
RETURNS SETOF outbox
LANGUAGE sql
VOLATILE
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
  SELECT o.*
  FROM public.outbox o
  WHERE NOT o.published
    AND (o.next_attempt_at IS NULL OR o.next_attempt_at <= now())
  ORDER BY o.created_at, o.id
  LIMIT greatest(least(p_limit, 1000), 0)
  FOR UPDATE SKIP LOCKED
$$;

CREATE OR REPLACE FUNCTION outbox_drain_stats()
RETURNS TABLE (dlq_depth bigint, drain_lag_seconds double precision)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
  SELECT (SELECT count(*) FROM public.outbox_dlq),
         (SELECT EXTRACT(EPOCH FROM (now() - min(o.created_at)))::double precision
            FROM public.outbox o
           WHERE NOT o.published)
$$;

ALTER FUNCTION outbox_claim_batch(int) OWNER TO dms_outbox_drain;
ALTER FUNCTION outbox_drain_stats()    OWNER TO dms_outbox_drain;

REVOKE ALL ON FUNCTION outbox_claim_batch(int) FROM PUBLIC;
REVOKE ALL ON FUNCTION outbox_drain_stats()    FROM PUBLIC;

GRANT EXECUTE ON FUNCTION outbox_claim_batch(int) TO dms_app;
GRANT EXECUTE ON FUNCTION outbox_drain_stats()    TO dms_app;

-- The drain itself runs as dms_app: it marks rows published, records
-- retries, deletes dead-lettered and expired rows, and inserts into
-- outbox_dlq. 000001 granted only SELECT/INSERT/UPDATE and 000094 granted
-- nothing on outbox_dlq, so do not rely on deployment default privileges.
GRANT SELECT, INSERT, UPDATE, DELETE ON outbox     TO dms_app;
GRANT INSERT                         ON outbox_dlq TO dms_app;

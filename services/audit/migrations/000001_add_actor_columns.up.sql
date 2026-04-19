-- §4.1 / A4 — reconcile audit_events schema with shipped handler code.
--
-- audit_events was created by services/document/migrations/000001 with
-- columns (id, tenant_id, actor_id, actor_type, ...). The audit service's
-- repository (services/audit/internal/repository/repository.go) instead
-- reads and writes `actor` + `actor_name` TEXT columns, and the
-- /api/v1/audit/events endpoint has been returning 500 since inception
-- because the column names the handler references do not exist.
--
-- Add the two columns with '' defaults (NOT NULL is safe here: every
-- existing row backfills to the default and the handler reads them as
-- non-nullable strings). The legacy actor_id / actor_type columns are
-- left in place — future cleanup can drop them once no reader references
-- them.
--
-- Idempotent: ADD COLUMN IF NOT EXISTS is a no-op on a re-run.

BEGIN;

ALTER TABLE audit_events
    ADD COLUMN IF NOT EXISTS actor      TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS actor_name TEXT NOT NULL DEFAULT '';

-- The handler's ListEvents supports filtering by actor; an index keeps
-- that query fast at 100M-row partitions.
CREATE INDEX IF NOT EXISTS idx_audit_events_actor
    ON audit_events (tenant_id, actor, created_at DESC);

COMMIT;

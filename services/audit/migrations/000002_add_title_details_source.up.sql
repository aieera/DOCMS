-- §4.1 / A4 follow-up — audit repo reads three more columns the
-- document-service migration didn't create: resource_title, details,
-- and source_event. Adding them unblocks GET /api/v1/audit/events
-- (currently 500 with "column \"resource_title\" does not exist").
--
-- All idempotent + NULLable so a backfill isn't strictly required;
-- JSONB default '{}' for `details` so the scan returns a usable
-- value instead of NULL for pre-existing rows.

BEGIN;

ALTER TABLE audit_events
    ADD COLUMN IF NOT EXISTS resource_title TEXT,
    ADD COLUMN IF NOT EXISTS details        JSONB NOT NULL DEFAULT '{}'::jsonb,
    ADD COLUMN IF NOT EXISTS source_event   TEXT;

COMMIT;

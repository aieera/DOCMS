-- §9.2 / G7 — GDPR Art 12(3) DSR deadline tracking.
--
-- GDPR mandates a 1-month turnaround on Art 15/16/17/20 requests,
-- extendable by 2 more months for complex cases (max 3 months
-- total). Without a due_at column the admin UI has no way to
-- surface at-risk tickets and the SRE team can't alert on them.
--
-- `due_at` defaults to `created_at + 30 days` at INSERT time.
-- Operators can PATCH the value to extend the deadline (up to
-- 90 days from created_at — enforced in the handler, not here).

BEGIN;

ALTER TABLE privacy_dsr_requests
    ADD COLUMN IF NOT EXISTS due_at TIMESTAMPTZ;

-- Backfill: existing rows get created_at + 30d. New rows default
-- via the handler writing the column explicitly.
UPDATE privacy_dsr_requests
SET due_at = created_at + INTERVAL '30 days'
WHERE due_at IS NULL;

-- Hot-path index for "what's due in the next 7 days": admin
-- dashboard + the daily alert CronJob both hit this ordering.
CREATE INDEX IF NOT EXISTS idx_dsr_pending_due
    ON privacy_dsr_requests (tenant_id, due_at)
    WHERE status IN ('pending', 'running');

COMMIT;

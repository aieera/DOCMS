-- Wave 20 — Stripe Meter reporting.
--
-- Additive column only; the wide-format usage_records shape from
-- 000010 is preserved. `reported_at` marks when the row's metrics
-- were last pushed to Stripe /v1/billing/meter_events. A row with
-- NULL reported_at is a candidate for the next push.
--
-- Partial index on (reported_at IS NULL) keeps the "due for report"
-- scan O(log n) in unreported rows only.
BEGIN;

ALTER TABLE usage_records
    ADD COLUMN IF NOT EXISTS reported_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS idx_usage_records_unreported
    ON usage_records(period_start)
 WHERE reported_at IS NULL;

COMMIT;

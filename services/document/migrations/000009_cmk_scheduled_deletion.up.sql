-- Wave 12.7: CMK scheduled-deletion column on tenant_keks.
--
-- When a tenant is de-provisioned, their KEKs shouldn't vanish
-- immediately — AWS KMS enforces a 7-30 day deletion window and
-- we want the same property on-prem. This column records the
-- scheduled deletion time; a daily cron (Wave 12.7b) reads rows
-- where scheduled_deletion_at < now() and physically drops the
-- master key material.
--
-- Operators trigger scheduling via the new control-plane endpoint
-- `POST /api/v1/admin/tenants/{id}/schedule-cmk-deletion { grace_hours }`.
-- A separate `POST .../cancel-cmk-deletion` clears the column
-- inside the grace window, restoring encryption for the tenant.
--
-- Index plan: partial index on rows with a pending deletion so
-- the cron sweep is tight.
BEGIN;

ALTER TABLE tenant_keks
    ADD COLUMN IF NOT EXISTS scheduled_deletion_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS scheduled_by          UUID;

CREATE INDEX IF NOT EXISTS idx_tenant_keks_scheduled_deletion
    ON tenant_keks (scheduled_deletion_at)
    WHERE scheduled_deletion_at IS NOT NULL;

COMMIT;

-- Migration 000013 — admin review lifecycle for quarantine_events.
--
-- 000012 created the audit-trail row that the storage finalize path writes
-- every time an object is moved to the quarantine bucket. Admins then act
-- on those rows via /admin/quarantine: mark reviewed, release, or delete.
-- The status column below tracks that review lifecycle; reviewed_by /
-- reviewed_at carry the attestation.

ALTER TABLE quarantine_events
    ADD COLUMN status TEXT NOT NULL DEFAULT 'new'
        CHECK (status IN ('new', 'reviewed', 'released', 'deleted')),
    ADD COLUMN reviewed_by UUID,
    ADD COLUMN reviewed_at TIMESTAMPTZ;

CREATE INDEX idx_quarantine_events_status
    ON quarantine_events(tenant_id, status, created_at DESC);

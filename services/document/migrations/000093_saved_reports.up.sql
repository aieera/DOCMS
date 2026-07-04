-- Saved analytics reports (ADR 0119): a stored analytics.Query spec +
-- presentation hint + optional schedule. Scheduled runs are driven by
-- Temporal Schedules in the workflow worker (one per enabled report,
-- reconciled every 60s — same pattern as saved-search alerts) and
-- deliver a notification with a link to the live report.

CREATE TABLE IF NOT EXISTS saved_reports (
    tenant_id        UUID NOT NULL REFERENCES organizations(id),
    id               UUID NOT NULL DEFAULT gen_random_uuid(),
    name             TEXT NOT NULL,
    description      TEXT NOT NULL DEFAULT '',
    query            JSONB NOT NULL,
    chart_type       TEXT NOT NULL DEFAULT 'table'
                         CHECK (chart_type IN ('table', 'bar', 'line', 'pie')),
    schedule_enabled BOOLEAN NOT NULL DEFAULT false,
    -- Cron wins when set; else every N minutes. Both empty/zero with
    -- schedule_enabled=true is rejected in the service layer.
    schedule_cron    TEXT NOT NULL DEFAULT '',
    schedule_interval_minutes INT NOT NULL DEFAULT 0,
    -- Delivery channels for the scheduled notification (the
    -- DeliveryPayload.channels consent hint): in_app | email | digest.
    channels         TEXT[] NOT NULL DEFAULT '{in_app}',
    created_by       UUID NOT NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_run_at      TIMESTAMPTZ,
    PRIMARY KEY (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS idx_saved_reports_name ON saved_reports (tenant_id, name);
CREATE INDEX IF NOT EXISTS idx_saved_reports_scheduled
    ON saved_reports (schedule_enabled) WHERE schedule_enabled = true;

ALTER TABLE saved_reports ENABLE ROW LEVEL SECURITY;
ALTER TABLE saved_reports FORCE ROW LEVEL SECURITY;
CREATE POLICY saved_reports_isolation ON saved_reports
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid)
    WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

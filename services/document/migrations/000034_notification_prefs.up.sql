-- ADR 0086 — unified notification preferences.
--
-- Three new tables + one column on the existing
-- notification_preferences row. Existing flat per-user preferences
-- keep working for back-compat; the new tables enable the
-- per-event-type matrix + snooze + DND + digest pipeline.

-- ---- 1. digest_enabled flag on each matrix cell -----------------------
-- The schema's notification_preferences already has the right
-- composite PK shape (tenant, user, channel, event_type); just
-- need a per-cell flag for "batch into digests".
ALTER TABLE notification_preferences
    ADD COLUMN IF NOT EXISTS digest_enabled BOOLEAN NOT NULL DEFAULT FALSE;


-- ---- 2. notification_snoozes -----------------------------------------
-- Explicit "mute event_type until X" overrides the matrix without
-- mutating it. event_type='*' = mute everything.
CREATE TABLE IF NOT EXISTS notification_snoozes (
    tenant_id   UUID         NOT NULL REFERENCES organizations(id),
    id          UUID         NOT NULL DEFAULT gen_random_uuid(),
    user_id     UUID         NOT NULL,
    event_type  TEXT         NOT NULL,
    until_at    TIMESTAMPTZ  NOT NULL,
    reason      TEXT,
    created_at  TIMESTAMPTZ  NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, user_id) REFERENCES users(tenant_id, id) ON DELETE CASCADE,
    CONSTRAINT notification_snoozes_until CHECK (until_at > created_at)
);

-- Hot-path query: "any active snooze for (user, event_type) right now".
CREATE INDEX IF NOT EXISTS idx_notification_snoozes_active
    ON notification_snoozes (tenant_id, user_id, event_type, until_at DESC);

ALTER TABLE notification_snoozes ENABLE ROW LEVEL SECURITY;
ALTER TABLE notification_snoozes FORCE  ROW LEVEL SECURITY;
DROP POLICY IF EXISTS notification_snoozes_tenant_isolation        ON notification_snoozes;
DROP POLICY IF EXISTS notification_snoozes_tenant_isolation_insert ON notification_snoozes;
CREATE POLICY notification_snoozes_tenant_isolation ON notification_snoozes
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY notification_snoozes_tenant_isolation_insert ON notification_snoozes
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);


-- ---- 3. notification_dnd ---------------------------------------------
-- One row per user. Composite PK (tenant, user) — single global
-- DND window per user, applied on top of the matrix.
CREATE TABLE IF NOT EXISTS notification_dnd (
    tenant_id    UUID         NOT NULL REFERENCES organizations(id),
    user_id      UUID         NOT NULL,
    dnd_start    TIME         NOT NULL,
    dnd_end      TIME         NOT NULL,
    -- Wall-clock tz so cross-tz tenants render the user's local
    -- evening hours correctly. IANA name (e.g. 'America/New_York').
    timezone     TEXT         NOT NULL DEFAULT 'UTC',
    created_at   TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ  NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, user_id),
    FOREIGN KEY (tenant_id, user_id) REFERENCES users(tenant_id, id) ON DELETE CASCADE
);

CREATE TRIGGER update_notification_dnd_updated_at
    BEFORE UPDATE ON notification_dnd FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

ALTER TABLE notification_dnd ENABLE ROW LEVEL SECURITY;
ALTER TABLE notification_dnd FORCE  ROW LEVEL SECURITY;
DROP POLICY IF EXISTS notification_dnd_tenant_isolation        ON notification_dnd;
DROP POLICY IF EXISTS notification_dnd_tenant_isolation_insert ON notification_dnd;
CREATE POLICY notification_dnd_tenant_isolation ON notification_dnd
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY notification_dnd_tenant_isolation_insert ON notification_dnd
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);


-- ---- 4. notification_digests -----------------------------------------
-- In-flight digest accumulator. One open (un-flushed) row per
-- (user, event_type, channel). The 1-minute flush ticker scans
-- for ready-to-flush rows and emits a single digest notification.
CREATE TABLE IF NOT EXISTS notification_digests (
    tenant_id        UUID         NOT NULL REFERENCES organizations(id),
    id               UUID         NOT NULL DEFAULT gen_random_uuid(),
    user_id          UUID         NOT NULL,
    event_type       TEXT         NOT NULL,
    channel          TEXT         NOT NULL,
    -- Array of event payloads accumulated during the 5-min window.
    events           JSONB        NOT NULL DEFAULT '[]'::jsonb,
    count            INTEGER      NOT NULL DEFAULT 0,
    -- First event's arrival + 5 min. Subsequent events fold in but
    -- DON'T extend the window (otherwise a slow trickle never
    -- flushes).
    flush_after_at   TIMESTAMPTZ  NOT NULL,
    flushed_at       TIMESTAMPTZ,
    created_at       TIMESTAMPTZ  NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, user_id) REFERENCES users(tenant_id, id) ON DELETE CASCADE
);

-- Partial unique index — at most ONE open (un-flushed) row per
-- (user, event_type, channel). Subsequent events in the window
-- find this row via the same key and append to the events array.
-- Once flushed_at is set the row no longer participates in the
-- uniqueness, so the next event opens a fresh row.
CREATE UNIQUE INDEX IF NOT EXISTS idx_notification_digests_open
    ON notification_digests (tenant_id, user_id, event_type, channel)
    WHERE flushed_at IS NULL;

-- Sweep query: "ready to flush right now".
CREATE INDEX IF NOT EXISTS idx_notification_digests_sweep
    ON notification_digests (tenant_id, flush_after_at)
    WHERE flushed_at IS NULL;

ALTER TABLE notification_digests ENABLE ROW LEVEL SECURITY;
ALTER TABLE notification_digests FORCE  ROW LEVEL SECURITY;
DROP POLICY IF EXISTS notification_digests_tenant_isolation        ON notification_digests;
DROP POLICY IF EXISTS notification_digests_tenant_isolation_insert ON notification_digests;
CREATE POLICY notification_digests_tenant_isolation ON notification_digests
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY notification_digests_tenant_isolation_insert ON notification_digests
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

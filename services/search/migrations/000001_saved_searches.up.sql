-- §4.1 / A4 — saved_searches table for /api/v1/saved-searches.
--
-- File existed pre-A4 but is now fully idempotent so `migrate up` is
-- safe on any state of the DB (fresh, partially-applied, fully-applied).
-- Also adds FORCE ROW LEVEL SECURITY to match the other tenant-scoped
-- tables in the repo (document service's 000001 sets this on every
-- tenant table — otherwise a table owner bypasses RLS on writes).

CREATE TABLE IF NOT EXISTS saved_searches (
    id              UUID        PRIMARY KEY,
    tenant_id       UUID        NOT NULL,
    user_id         UUID        NOT NULL,
    name            TEXT        NOT NULL,
    query           TEXT        NOT NULL DEFAULT '',
    filters         JSONB       NOT NULL DEFAULT '{}',
    notify          BOOLEAN     NOT NULL DEFAULT false,
    notify_interval_minutes INT NOT NULL DEFAULT 15,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_run_at     TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_saved_searches_tenant_user
    ON saved_searches (tenant_id, user_id);
CREATE INDEX IF NOT EXISTS idx_saved_searches_notifiable
    ON saved_searches (notify, last_run_at)
    WHERE notify = true;

ALTER TABLE saved_searches ENABLE ROW LEVEL SECURITY;
ALTER TABLE saved_searches FORCE  ROW LEVEL SECURITY;

DROP POLICY IF EXISTS saved_searches_tenant_isolation        ON saved_searches;
DROP POLICY IF EXISTS saved_searches_tenant_isolation_insert ON saved_searches;

CREATE POLICY saved_searches_tenant_isolation ON saved_searches
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY saved_searches_tenant_isolation_insert ON saved_searches
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

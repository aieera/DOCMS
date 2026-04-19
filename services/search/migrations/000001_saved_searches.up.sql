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

CREATE INDEX idx_saved_searches_tenant_user ON saved_searches (tenant_id, user_id);
CREATE INDEX idx_saved_searches_notifiable ON saved_searches (notify, last_run_at)
    WHERE notify = true;

ALTER TABLE saved_searches ENABLE ROW LEVEL SECURITY;
CREATE POLICY saved_searches_tenant_isolation ON saved_searches
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);

-- ADR 0068 — saved-search alerting + subscribers.
--
-- Builds on the existing saved_searches table from 000001. Two new
-- shapes:
--   1. saved_search_subscribers — many-to-many join keyed on
--      (saved_search_id, user_id) so an alert can fan out to a team
--      without each member having to clone the search.
--   2. last_match_doc_ids on saved_searches — the diff cursor. The
--      Temporal alert workflow stores the previous run's hits here
--      and emits notifications only for doc_ids that weren't in the
--      last set.

ALTER TABLE saved_searches
    -- JSONB array of doc_id strings from the most recent run. The
    -- alert workflow diffs the current run against this and emits
    -- one notification per NEW doc_id. Empty array on first run
    -- (no false-positive flood the moment an alert is created).
    ADD COLUMN IF NOT EXISTS last_match_doc_ids JSONB NOT NULL DEFAULT '[]'::jsonb,
    -- ADR 0068 calls this `is_alert` in the spec; we keep the
    -- existing `notify` boolean column with the same semantics.
    -- Adding alert_frequency_cron in addition to the legacy
    -- notify_interval_minutes so admins can specify "9am every
    -- weekday" alongside the simple "every 15 min" knob.
    ADD COLUMN IF NOT EXISTS alert_frequency_cron TEXT,
    -- The Temporal workflow handle. Recorded so a delete or
    -- pause can locate the running schedule without a search.
    ADD COLUMN IF NOT EXISTS workflow_id TEXT;


CREATE TABLE IF NOT EXISTS saved_search_subscribers (
    tenant_id        UUID        NOT NULL,
    saved_search_id  UUID        NOT NULL,
    user_id          UUID        NOT NULL,
    -- channels selects how this subscriber wants to be notified.
    -- Valid values mirror the notifications service's delivery
    -- modes. Multiple allowed: a user can want both 'in_app' AND
    -- 'email' for the same alert.
    channels         TEXT[]      NOT NULL DEFAULT ARRAY['in_app']::TEXT[],
    -- subscribed_by tracks who added this subscription — the alert
    -- owner (typical case) or an admin doing bulk subscribe. Audit
    -- trail without needing to read the audit log.
    subscribed_by    UUID        NOT NULL,
    subscribed_at    TIMESTAMPTZ NOT NULL DEFAULT now(),

    PRIMARY KEY (tenant_id, saved_search_id, user_id),
    CONSTRAINT saved_search_subscribers_channels_nonempty
        CHECK (cardinality(channels) > 0)
);

CREATE INDEX IF NOT EXISTS idx_saved_search_subscribers_user
    ON saved_search_subscribers (tenant_id, user_id);

ALTER TABLE saved_search_subscribers ENABLE ROW LEVEL SECURITY;
ALTER TABLE saved_search_subscribers FORCE  ROW LEVEL SECURITY;

DROP POLICY IF EXISTS sss_tenant_isolation        ON saved_search_subscribers;
DROP POLICY IF EXISTS sss_tenant_isolation_insert ON saved_search_subscribers;

CREATE POLICY sss_tenant_isolation ON saved_search_subscribers
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY sss_tenant_isolation_insert ON saved_search_subscribers
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

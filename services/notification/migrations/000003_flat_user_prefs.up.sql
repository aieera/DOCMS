-- Review fix (ADR 0117 hardening): the flat per-user preference row the
-- notification service's GetPreference/UpsertPreference queries never
-- had a table — notification_preferences (document migration 000001) is
-- MATRIX-shaped (tenant, user, channel, event_type). The flat SELECT
-- errored on every call ("column email_enabled does not exist"), the
-- error was swallowed, and the zero-value pref silently disabled every
-- email and push delivery. This table gives the flat surface a real
-- home; defaults are enabled so delivery is opt-out, matching the
-- repository's ErrNoRows default.
--
-- No RLS: this service queries the pool directly with explicit
-- tenant_id predicates (same convention as push_devices / 000002).

BEGIN;

CREATE TABLE IF NOT EXISTS notification_user_prefs (
    tenant_id        UUID    NOT NULL,
    user_id          UUID    NOT NULL,
    email_enabled    BOOLEAN NOT NULL DEFAULT true,
    push_enabled     BOOLEAN NOT NULL DEFAULT true,
    slack_enabled    BOOLEAN NOT NULL DEFAULT false,
    sms_enabled      BOOLEAN NOT NULL DEFAULT false,
    quiet_hours_from INT     NOT NULL DEFAULT 0,
    quiet_hours_to   INT     NOT NULL DEFAULT 0,
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, user_id),
    FOREIGN KEY (tenant_id, user_id) REFERENCES users (tenant_id, id) ON DELETE CASCADE
);

COMMIT;

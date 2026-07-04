-- ADR 0117 — mobile push device registry.
--
-- One row per (tenant, user, push token). The mobile app registers its
-- ExponentPushToken after login (POST /api/v1/notifications/devices);
-- the delivery fan-out reads active rows and revokes tokens Expo
-- reports as DeviceNotRegistered so the fleet self-heals.
--
-- No RLS here: the notification service queries the pool directly with
-- explicit tenant_id predicates (same convention as the notifications /
-- notification_preferences tables it already owns — it never sets the
-- app.current_tenant GUC, so a FORCE RLS policy would fail-closed its
-- every read).

BEGIN;

CREATE TABLE IF NOT EXISTS push_devices (
    tenant_id    UUID        NOT NULL,
    id           UUID        NOT NULL DEFAULT gen_random_uuid(),
    user_id      UUID        NOT NULL,
    platform     TEXT        NOT NULL DEFAULT 'expo' CHECK (platform IN ('expo', 'fcm', 'apns')),
    token        TEXT        NOT NULL,
    label        TEXT        NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    revoked_at   TIMESTAMPTZ,
    PRIMARY KEY (tenant_id, id),
    -- Offboarding a user must drop their push targets (same lifecycle
    -- rule as notification_snoozes / _dnd / _digests in 000034).
    FOREIGN KEY (tenant_id, user_id) REFERENCES users (tenant_id, id) ON DELETE CASCADE,
    -- A token belongs to at most one registration per tenant; a
    -- re-register (same physical device, maybe a different user after
    -- logout/login) updates the row instead of duplicating it.
    UNIQUE (tenant_id, token)
);

CREATE INDEX IF NOT EXISTS idx_push_devices_user
    ON push_devices (tenant_id, user_id)
    WHERE revoked_at IS NULL;

COMMIT;

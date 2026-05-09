-- ADR 0063 — Complete MFA surface.
--
-- Three new tables:
--   user_mfa_methods    — one row per (user, method) for totp/push/email/sms
--   user_push_devices   — FCM/APNs device tokens for the push factor
--   tenant_mfa_policy   — per-tenant policy + allowed-methods set
--
-- The legacy users.mfa_secret_encrypted + users.mfa_recovery_hashes
-- columns stay in place; the service layer treats user_mfa_methods
-- as authoritative when present and lazily backfills the legacy
-- TOTP secret on next successful verify.

-- ---- user_mfa_methods --------------------------------------------------
CREATE TABLE IF NOT EXISTS user_mfa_methods (
    tenant_id          UUID         NOT NULL REFERENCES organizations(id),
    user_id            UUID         NOT NULL,
    -- One of: totp | sms | email | push.
    -- passkey is intentionally NOT modelled here — it lives in
    -- webauthn_credentials (ADR 0061) and uses its own life cycle.
    method             TEXT         NOT NULL CHECK (method IN ('totp', 'sms', 'email', 'push')),
    status             TEXT         NOT NULL DEFAULT 'pending'
                                    CHECK (status IN ('pending', 'active', 'suspended')),
    -- Method-specific destination / secret. Nullable per method:
    --   totp  → secret_encrypted populated
    --   sms   → phone_e164 populated
    --   email → email populated (may differ from users.email)
    --   push  → no secret here; per-device rows in user_push_devices
    secret_encrypted   BYTEA,
    phone_e164         TEXT,
    email              TEXT,
    -- 8 single-use bcrypt-hashed recovery codes per ADR 0063. Empty
    -- array on creation; populated on first activation. Service-
    -- layer regenerates on user request via /mfa/recovery-codes.
    recovery_hashes    TEXT[]       NOT NULL DEFAULT ARRAY[]::TEXT[],
    -- Bumped on every verify failure; reset on success. Cooldown is
    -- enforced in the service layer via the same Redis rate-limit
    -- key the legacy TOTP path uses.
    failed_count       INTEGER      NOT NULL DEFAULT 0,
    last_used_at       TIMESTAMPTZ,
    created_at         TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ  NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, user_id, method),
    FOREIGN KEY (tenant_id, user_id) REFERENCES users(tenant_id, id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_user_mfa_methods_active
    ON user_mfa_methods (tenant_id, user_id) WHERE status = 'active';

CREATE TRIGGER update_user_mfa_methods_updated_at
    BEFORE UPDATE ON user_mfa_methods FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

ALTER TABLE user_mfa_methods ENABLE ROW LEVEL SECURITY;
ALTER TABLE user_mfa_methods FORCE  ROW LEVEL SECURITY;
DROP POLICY IF EXISTS user_mfa_methods_tenant_isolation        ON user_mfa_methods;
DROP POLICY IF EXISTS user_mfa_methods_tenant_isolation_insert ON user_mfa_methods;
CREATE POLICY user_mfa_methods_tenant_isolation ON user_mfa_methods
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY user_mfa_methods_tenant_isolation_insert ON user_mfa_methods
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);


-- ---- user_push_devices -------------------------------------------------
-- Per-device FCM/APNs registration. One user can have multiple
-- devices (phone + tablet); the push challenge fans out to all
-- non-revoked devices and the first ack wins.
CREATE TABLE IF NOT EXISTS user_push_devices (
    tenant_id     UUID         NOT NULL REFERENCES organizations(id),
    id            UUID         NOT NULL DEFAULT gen_random_uuid(),
    user_id       UUID         NOT NULL,
    -- 'fcm' (Android, web) | 'apns' (iOS / macOS Safari).
    platform      TEXT         NOT NULL CHECK (platform IN ('fcm', 'apns')),
    -- Vendor-issued opaque token. Length varies wildly by vendor;
    -- stored as TEXT so the column doesn't need a migration if the
    -- vendor changes its format.
    token         TEXT         NOT NULL,
    -- Per-device HMAC key the device signs its push-ack with. Sealed
    -- with the per-tenant KEK on insert; rotated when the device
    -- re-registers.
    ack_key_sealed BYTEA       NOT NULL,
    -- Display name surfaced in the security UI: "iPhone 15 (Work)".
    label         TEXT         NOT NULL,
    created_at    TIMESTAMPTZ  NOT NULL DEFAULT now(),
    last_used_at  TIMESTAMPTZ,
    revoked_at    TIMESTAMPTZ,
    PRIMARY KEY (tenant_id, id),
    UNIQUE (tenant_id, user_id, token),
    FOREIGN KEY (tenant_id, user_id) REFERENCES users(tenant_id, id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_user_push_devices_user
    ON user_push_devices (tenant_id, user_id) WHERE revoked_at IS NULL;

ALTER TABLE user_push_devices ENABLE ROW LEVEL SECURITY;
ALTER TABLE user_push_devices FORCE  ROW LEVEL SECURITY;
DROP POLICY IF EXISTS user_push_devices_tenant_isolation        ON user_push_devices;
DROP POLICY IF EXISTS user_push_devices_tenant_isolation_insert ON user_push_devices;
CREATE POLICY user_push_devices_tenant_isolation ON user_push_devices
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY user_push_devices_tenant_isolation_insert ON user_push_devices
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);


-- ---- tenant_mfa_policy -------------------------------------------------
-- One row per tenant. Default mode is 'optional' until an admin
-- changes it.
CREATE TABLE IF NOT EXISTS tenant_mfa_policy (
    tenant_id            UUID         PRIMARY KEY REFERENCES organizations(id) ON DELETE CASCADE,
    -- disabled    : MFA cannot be enrolled or required
    -- optional    : users may enroll, login does not force
    -- required    : every user must have ≥1 active method to log in
    -- conditional : MFA gate fires only on the actions in
    --               conditional_actions (matches the step-up scope
    --               set from ADR 0061)
    mode                 TEXT         NOT NULL DEFAULT 'optional'
                                       CHECK (mode IN ('disabled', 'optional', 'required', 'conditional')),
    -- Subset of {passkey, totp, push, email, sms} the tenant lets
    -- users enroll. NULL/empty = all methods allowed.
    allowed_methods      TEXT[]       NOT NULL DEFAULT ARRAY['passkey','totp','push','email','sms']::TEXT[],
    -- When mode='conditional', these scopes (matching step_up_grants.scope)
    -- demand a fresh MFA proof before the action proceeds. Examples:
    -- 'legal_hold:release', 'dsr:erase_approve', 'sso_admin:write'.
    conditional_actions  TEXT[]       NOT NULL DEFAULT ARRAY[]::TEXT[],
    -- Operator who flipped the policy last; surfaced in the audit
    -- log so a regulator-facing review knows who changed it.
    updated_by           UUID,
    created_at           TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at           TIMESTAMPTZ  NOT NULL DEFAULT now()
);

CREATE TRIGGER update_tenant_mfa_policy_updated_at
    BEFORE UPDATE ON tenant_mfa_policy FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

ALTER TABLE tenant_mfa_policy ENABLE ROW LEVEL SECURITY;
ALTER TABLE tenant_mfa_policy FORCE  ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_mfa_policy_tenant_isolation        ON tenant_mfa_policy;
DROP POLICY IF EXISTS tenant_mfa_policy_tenant_isolation_insert ON tenant_mfa_policy;
CREATE POLICY tenant_mfa_policy_tenant_isolation ON tenant_mfa_policy
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY tenant_mfa_policy_tenant_isolation_insert ON tenant_mfa_policy
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);


-- step_up_grants.granted_via already takes any text; ADR 0063 widens
-- the legal value set to include {totp, push, email, sms} alongside
-- the existing 'webauthn'. No DDL change needed — the column has no
-- CHECK constraint on the value set.

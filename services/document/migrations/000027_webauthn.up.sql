-- ADR 0061 — WebAuthn / Passkeys.
--
-- Two new tables:
--   webauthn_credentials  — public-key creds per user
--   step_up_grants        — fresh-presence window for sensitive ops

CREATE TABLE IF NOT EXISTS webauthn_credentials (
    tenant_id            UUID         NOT NULL REFERENCES organizations(id),
    -- The credential id is the unique identifier the authenticator
    -- chose; we use it as PK so a user re-registering the same
    -- authenticator is an UPDATE, not a duplicate.
    credential_id        BYTEA        NOT NULL,
    user_id              UUID         NOT NULL,
    -- Public key in COSE format (CBOR). The webauthn lib stores it
    -- as bytes; we never decode server-side, only pass back at
    -- assertion verification.
    public_key           BYTEA        NOT NULL,
    -- Signature counter — bumped on every successful assertion.
    -- A counter that rolls BACKWARD signals a cloned authenticator
    -- and we reject the assertion.
    sign_count           BIGINT       NOT NULL DEFAULT 0,
    -- Authenticator GUID — identifies the authenticator model
    -- (e.g. Yubikey 5, Apple Passkey, Windows Hello). Useful for
    -- the security UI ("Yubikey 5 NFC, added 3 days ago").
    aaguid               BYTEA,
    -- Transports the authenticator advertised: usb, nfc, ble,
    -- internal, hybrid. Stored verbatim from the registration
    -- response so subsequent assertion calls can hint to the
    -- client which transport to use first.
    transports           TEXT[]       NOT NULL DEFAULT ARRAY[]::TEXT[],
    -- Friendly name the user typed in the security settings page
    -- ("Work laptop", "Phone", "Yubikey at desk"). Required —
    -- the security UI never shows a credential without a name.
    name                 TEXT         NOT NULL,
    -- Backed-up flag — true when the authenticator syncs the
    -- credential across devices (Apple/Google Passkeys).
    backup_eligible      BOOLEAN      NOT NULL DEFAULT FALSE,
    backup_state         BOOLEAN      NOT NULL DEFAULT FALSE,
    -- Timestamps for the security UI's "added X ago" + "last used Y ago".
    created_at           TIMESTAMPTZ  NOT NULL DEFAULT now(),
    last_used_at         TIMESTAMPTZ,

    PRIMARY KEY (tenant_id, credential_id),
    FOREIGN KEY (tenant_id, user_id) REFERENCES users(tenant_id, id)
);

-- Lookup index on (tenant, user) — the assertion path needs every
-- cred for a given user.
CREATE INDEX IF NOT EXISTS idx_webauthn_credentials_user
    ON webauthn_credentials (tenant_id, user_id);

ALTER TABLE webauthn_credentials ENABLE ROW LEVEL SECURITY;
ALTER TABLE webauthn_credentials FORCE  ROW LEVEL SECURITY;
DROP POLICY IF EXISTS webauthn_credentials_tenant_isolation        ON webauthn_credentials;
DROP POLICY IF EXISTS webauthn_credentials_tenant_isolation_insert ON webauthn_credentials;
CREATE POLICY webauthn_credentials_tenant_isolation ON webauthn_credentials
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY webauthn_credentials_tenant_isolation_insert ON webauthn_credentials
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);


-- ---- step_up_grants ---------------------------------------------------
-- Fresh-presence window: a recent passkey assertion grants the user
-- the right to perform sensitive ops for the next 5 minutes.
-- Sensitive routes (legal-hold release, DSR erase approval,
-- quarantine release) check this table; missing or expired = 403
-- with a "step-up required" hint that the UI maps to a passkey
-- prompt.
CREATE TABLE IF NOT EXISTS step_up_grants (
    tenant_id    UUID        NOT NULL REFERENCES organizations(id),
    user_id      UUID        NOT NULL,
    -- Optional scope narrowing — empty/NULL means "all sensitive
    -- ops". Future-proofing for per-op scopes ("legal_hold:release").
    scope        TEXT        NOT NULL DEFAULT '',
    -- granted_via: the auth method that produced this grant.
    -- "webauthn" today; future "totp_fresh", "sso_recent", etc.
    granted_via  TEXT        NOT NULL,
    -- credential_id linking back to webauthn_credentials when via=webauthn.
    -- NULL for other granted_via values.
    credential_id BYTEA,
    granted_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Hard expiry — the middleware computes "is now() < expires_at".
    -- 5-minute default per ADR 0061 §"Step-up auth"; routes can
    -- request a tighter window via a service-layer parameter but
    -- not a wider one.
    expires_at   TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (tenant_id, user_id, scope, granted_at)
);

-- Non-partial index — Postgres rejects `now()` in WHERE because
-- the function isn't IMMUTABLE. The lookup query filters
-- expires_at > now() at query time; the btree on
-- (tenant, user, expires_at DESC) makes that scan cheap.
CREATE INDEX IF NOT EXISTS idx_step_up_grants_active
    ON step_up_grants (tenant_id, user_id, expires_at DESC);

ALTER TABLE step_up_grants ENABLE ROW LEVEL SECURITY;
ALTER TABLE step_up_grants FORCE  ROW LEVEL SECURITY;
DROP POLICY IF EXISTS step_up_grants_tenant_isolation        ON step_up_grants;
DROP POLICY IF EXISTS step_up_grants_tenant_isolation_insert ON step_up_grants;
CREATE POLICY step_up_grants_tenant_isolation ON step_up_grants
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY step_up_grants_tenant_isolation_insert ON step_up_grants
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

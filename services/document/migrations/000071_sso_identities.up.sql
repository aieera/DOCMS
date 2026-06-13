-- SSO identity link table — account-takeover hardening (blueprint §24.1
-- "M365 takeover hardening: replace email mapping with a (tid, oid) link").
--
-- Before this, SSO login (oidc.go / saml.go -> FindOrCreateSAMLUser)
-- matched the IdP assertion to a SeDoc account purely by EMAIL. Email is
-- mutable and reassignable in the IdP, so an admin (or attacker) who
-- reassigns a former employee's address to a new principal could log in
-- and inherit the old account — a silent account takeover.
--
-- This table pins each SeDoc user to the IdP's IMMUTABLE subject
-- (OIDC `sub` / Entra object id, SAML NameID) per provider. The service
-- resolves by (tenant, provider, subject) first and only falls back to
-- email for the first-time link; once linked, a different subject with
-- the same email can no longer take the account over.

CREATE TABLE IF NOT EXISTS sso_identities (
    tenant_id     UUID        NOT NULL REFERENCES organizations(id),
    provider      TEXT        NOT NULL CHECK (provider IN ('oidc', 'saml')),
    -- The IdP's stable subject identifier. Never an email.
    subject       TEXT        NOT NULL,
    user_id       UUID        NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_login_at TIMESTAMPTZ,
    PRIMARY KEY (tenant_id, provider, subject),
    FOREIGN KEY (tenant_id, user_id)
        REFERENCES users (tenant_id, id) ON DELETE CASCADE
);

-- A user links to exactly one subject per provider. This is the
-- defense-in-depth twin of the takeover guard in the service: trying to
-- attach a second subject to an already-linked user fails at the DB.
CREATE UNIQUE INDEX IF NOT EXISTS uq_sso_identities_user_provider
    ON sso_identities (tenant_id, provider, user_id);

ALTER TABLE sso_identities ENABLE ROW LEVEL SECURITY;
ALTER TABLE sso_identities FORCE  ROW LEVEL SECURITY;
CREATE POLICY sso_identities_tenant_isolation ON sso_identities
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid)
    WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

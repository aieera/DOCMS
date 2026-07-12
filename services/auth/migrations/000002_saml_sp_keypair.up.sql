-- Durable SAML Service-Provider identity.
--
-- The auth service used to generate a fresh self-signed SP key+cert on
-- EVERY boot (main.go sso.NewSelfSignedSP), so the SP certificate changed
-- on each restart. IdPs that pin the SP cert (signature validation on
-- AuthnRequests / metadata) broke on every restart — intermittent SSO
-- outages that looked like IdP flakiness.
--
-- This table holds the ONE service-wide SP keypair, generated once on
-- first boot and reused across restarts. The private key is sealed under
-- the deployment KEK (same envelope pattern as MFA secrets / LDAP bind
-- passwords); the certificate is public (it's what IdP admins pin) and
-- stored in clear PEM. Singleton: the `id` column is a fixed TRUE so
-- there is at most one active row; rotation overwrites it (bumping
-- rotated_at) — a deliberate, operator-driven change, not a per-restart
-- surprise.
CREATE TABLE IF NOT EXISTS saml_sp_keypair (
    id                  BOOLEAN     PRIMARY KEY DEFAULT TRUE CHECK (id),
    private_key_sealed  BYTEA       NOT NULL,   -- nonce||ciphertext under the KEK
    certificate_pem     TEXT        NOT NULL,   -- public; served in SP metadata
    fingerprint         TEXT        NOT NULL,   -- SHA-256 of the cert DER (hex, colon-separated)
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    rotated_at          TIMESTAMPTZ             -- set when an operator rotates
);

-- Not tenant-scoped (a single service identity), so no RLS. The app role
-- reads it on boot and writes it on first-boot generation / rotation.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'dms_app') THEN
        GRANT SELECT, INSERT, UPDATE ON saml_sp_keypair TO dms_app;
    END IF;
END $$;

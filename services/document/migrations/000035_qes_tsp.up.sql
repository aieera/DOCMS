-- ADR 0070 — eIDAS QES via Swisscom / Intesi / InfoCert.
--
-- Two new tables (tsp_signing_sessions, qes_certificates) plus a
-- widening of the existing signature_requests.provider CHECK so a
-- request can be marked qes_<tsp> and routed through the QTSP
-- ceremony instead of the internal/DocuSign/Adobe paths.

-- ---- 1. signature_requests.provider widening ------------------------
ALTER TABLE signature_requests DROP CONSTRAINT IF EXISTS signature_requests_provider_check;
ALTER TABLE signature_requests
    ADD CONSTRAINT signature_requests_provider_check
    CHECK (provider IN (
        'internal', 'docusign', 'adobe_sign',
        'qes_swisscom', 'qes_intesi', 'qes_infocert'
    ));


-- ---- 2. tsp_signing_sessions ----------------------------------------
-- One row per redirect ceremony. status drives the UI:
--   pending     — Authorize() called, redirect URL handed to user,
--                 waiting for QTSP-side return.
--   authorized  — return-URL hit; we have the auth code + are
--                 calling Sign() on the TSP. Brief.
--   completed   — signed hash embedded, qes_certificates row written.
--   failed      — TSP-reported error or our embedding failed.
--   expires_at  — we drop pending/authorized rows past this; the
--                 reaper marks them 'expired'.
CREATE TABLE IF NOT EXISTS tsp_signing_sessions (
    tenant_id      UUID         NOT NULL REFERENCES organizations(id),
    id             UUID         NOT NULL DEFAULT gen_random_uuid(),
    request_id     UUID         NOT NULL,
    signer_id      UUID         NOT NULL,
    provider       TEXT         NOT NULL CHECK (provider IN ('swisscom','intesi','infocert')),
    status         TEXT         NOT NULL DEFAULT 'pending'
                        CHECK (status IN ('pending','authorized','completed','failed','expired')),
    document_hash  TEXT         NOT NULL,           -- hex SHA-256
    external_id    TEXT,                            -- TSP-side transaction id
    redirect_url   TEXT         NOT NULL,
    return_url     TEXT         NOT NULL,
    state_secret   TEXT         NOT NULL,           -- HMAC seed for return-URL anti-tamper
    failure_reason TEXT,
    created_at     TIMESTAMPTZ  NOT NULL DEFAULT now(),
    authorized_at  TIMESTAMPTZ,
    completed_at   TIMESTAMPTZ,
    expires_at     TIMESTAMPTZ  NOT NULL,
    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, request_id) REFERENCES signature_requests(tenant_id, id),
    FOREIGN KEY (tenant_id, signer_id)  REFERENCES signature_signers(tenant_id, id)
);

CREATE INDEX IF NOT EXISTS idx_tsp_sessions_request ON tsp_signing_sessions(tenant_id, request_id);
-- Reaper's hot-path query: pending or authorized + past-expiry.
CREATE INDEX IF NOT EXISTS idx_tsp_sessions_pending_expiry
    ON tsp_signing_sessions(expires_at)
    WHERE status IN ('pending','authorized');

ALTER TABLE tsp_signing_sessions ENABLE ROW LEVEL SECURITY;
ALTER TABLE tsp_signing_sessions FORCE  ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tsp_sessions_tenant_isolation        ON tsp_signing_sessions;
DROP POLICY IF EXISTS tsp_sessions_tenant_isolation_insert ON tsp_signing_sessions;
CREATE POLICY tsp_sessions_tenant_isolation ON tsp_signing_sessions
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY tsp_sessions_tenant_isolation_insert ON tsp_signing_sessions
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);


-- ---- 3. qes_certificates --------------------------------------------
-- One row per successful QES sign. Holds the leaf cert + chain + the
-- LTV revocation material captured at sign time. Used for the cert-
-- display UI block AND by the signer sidecar to populate the PDF's
-- DSS dictionary (so verification stays offline-safe even if the
-- QTSP's OCSP responder is down later).
CREATE TABLE IF NOT EXISTS qes_certificates (
    tenant_id       UUID         NOT NULL REFERENCES organizations(id),
    id              UUID         NOT NULL DEFAULT gen_random_uuid(),
    signer_id       UUID         NOT NULL,
    request_id      UUID         NOT NULL,
    session_id      UUID         NOT NULL,
    provider        TEXT         NOT NULL,
    subject_dn      TEXT         NOT NULL,
    issuer_dn       TEXT         NOT NULL,
    serial_hex      TEXT         NOT NULL,
    not_before      TIMESTAMPTZ  NOT NULL,
    not_after       TIMESTAMPTZ  NOT NULL,
    cert_pem        TEXT         NOT NULL,
    chain_pem       TEXT,
    ltv_revocation  JSONB,
    created_at      TIMESTAMPTZ  NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, request_id) REFERENCES signature_requests(tenant_id, id),
    FOREIGN KEY (tenant_id, signer_id)  REFERENCES signature_signers(tenant_id, id),
    FOREIGN KEY (tenant_id, session_id) REFERENCES tsp_signing_sessions(tenant_id, id)
);

CREATE INDEX IF NOT EXISTS idx_qes_certs_request ON qes_certificates(tenant_id, request_id);
CREATE INDEX IF NOT EXISTS idx_qes_certs_signer  ON qes_certificates(tenant_id, signer_id);

ALTER TABLE qes_certificates ENABLE ROW LEVEL SECURITY;
ALTER TABLE qes_certificates FORCE  ROW LEVEL SECURITY;
DROP POLICY IF EXISTS qes_certs_tenant_isolation        ON qes_certificates;
DROP POLICY IF EXISTS qes_certs_tenant_isolation_insert ON qes_certificates;
CREATE POLICY qes_certs_tenant_isolation ON qes_certificates
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY qes_certs_tenant_isolation_insert ON qes_certificates
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

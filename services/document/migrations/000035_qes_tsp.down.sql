-- Reverse of 000035. Drops the two QES tables and reverts the
-- signature_requests provider CHECK to its pre-ADR-0070 set.

DROP TABLE IF EXISTS qes_certificates;
DROP TABLE IF EXISTS tsp_signing_sessions;

ALTER TABLE signature_requests DROP CONSTRAINT IF EXISTS signature_requests_provider_check;
ALTER TABLE signature_requests
    ADD CONSTRAINT signature_requests_provider_check
    CHECK (provider IN ('internal', 'docusign', 'adobe_sign'));

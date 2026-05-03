-- ADR 0061 follow-up — store the per-tenant LLM API key in ner_config
-- so admins can manage it from the UI without restarting workers or
-- editing .env. Encrypted at rest with the tenant KEK (AES-256-GCM,
-- base64(nonce || ciphertext) — same wire format the auth service uses
-- for MFA secrets, see services/auth/internal/service/mfa.go).
--
-- We deliberately do NOT add a CHECK constraint on the encrypted blob
-- length; rotating to a different KEK / cipher would otherwise be a
-- breaking schema change.

ALTER TABLE ner_config
    ADD COLUMN llm_api_key_encrypted TEXT,
    ADD COLUMN llm_api_key_set_at    TIMESTAMPTZ;

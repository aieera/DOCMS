-- ADR 0073 — Mobile + in-person signing modes.
--
-- The remote-only signature ceremony grows two siblings:
--   1. mobile  — same magic-link flow, finger-drawn SVG capture,
--                touch-first UI.
--   2. in_person — single device, sequential signer + witness on
--                  the same session.
--
-- Schema additions:
--   * signature_requests.signing_mode  — discriminator for which
--     ceremony was selected at request creation.
--   * signature_requests.final_hash_sha256 — document-bytes hash
--     captured at completion. The Verify endpoint re-hashes and
--     compares; mismatch = blob swap detected.
--   * signature_signers.signature_svg_path — biometric capture from
--     the touch canvas (SVG <path d=...> string). Stored as text so
--     it scales without raster artifacts and stays small (~2–5 KB).
--   * signature_signers.signed_doc_hash_sha256 — per-signer hash so
--     mid-ceremony tampering between signer and witness is detectable.
--   * signature_signers.device_kind — phone / tablet / desktop, set
--     by the client. Evidence-only; not a security boundary.

ALTER TABLE signature_requests
    ADD COLUMN IF NOT EXISTS signing_mode TEXT NOT NULL DEFAULT 'remote'
        CHECK (signing_mode IN ('remote', 'mobile', 'in_person')),
    ADD COLUMN IF NOT EXISTS final_hash_sha256 TEXT;

ALTER TABLE signature_signers
    ADD COLUMN IF NOT EXISTS signature_svg_path TEXT,
    ADD COLUMN IF NOT EXISTS signed_doc_hash_sha256 TEXT,
    ADD COLUMN IF NOT EXISTS device_kind TEXT
        CHECK (device_kind IS NULL OR device_kind IN ('phone', 'tablet', 'desktop'));

-- The witness role is already in the original CHECK constraint
-- (signature_signers.role) — no change needed there. The in_person
-- ceremony just means the UI sequences signer + witness on one
-- device; the schema doesn't care.

CREATE INDEX IF NOT EXISTS idx_sig_req_signing_mode
    ON signature_requests(tenant_id, signing_mode)
    WHERE signing_mode <> 'remote';

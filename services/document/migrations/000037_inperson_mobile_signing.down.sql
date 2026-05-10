-- ADR 0073 — rollback for mobile + in-person signing additions.

DROP INDEX IF EXISTS idx_sig_req_signing_mode;

ALTER TABLE signature_signers
    DROP COLUMN IF EXISTS device_kind,
    DROP COLUMN IF EXISTS signed_doc_hash_sha256,
    DROP COLUMN IF EXISTS signature_svg_path;

ALTER TABLE signature_requests
    DROP COLUMN IF EXISTS final_hash_sha256,
    DROP COLUMN IF EXISTS signing_mode;

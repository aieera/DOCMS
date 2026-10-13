DROP FUNCTION IF EXISTS zt_share_lookup_by_token_hash(bytea);
REVOKE SELECT ON zt_share_tokens FROM dms_token_lookup;

DROP INDEX IF EXISTS idx_zt_share_tokens_hash;
CREATE INDEX IF NOT EXISTS idx_zt_share_tokens_hash ON zt_share_tokens (token_hash);

-- The token_id rotation is one-way: the URL pre-images were never stored
-- after the up migration, so they cannot be restored. Code from before
-- 000113 looks links up by (token_id, token_hash) and will therefore not
-- resolve links whose token_id was rotated or minted after 000113.

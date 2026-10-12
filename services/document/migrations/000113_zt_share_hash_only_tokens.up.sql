-- D-9 — zero-trust share tokens: store only the hash.
--
-- Until now zt_share_tokens.token_id was BOTH the primary key and the URL
-- credential (/zt/{token_id}); token_hash = sha256(token_id || pepper) sat
-- beside it. Anyone who can read the table (a DB reader, a backup, support
-- tooling) could rebuild every live share URL from token_id alone, and the
-- watermark text embedded its first 8 hex chars.
--
-- From this migration on the URL carries a random pre-image that is never
-- stored; token_id is a separate random row id (the sender's admin handle
-- for revoke/telemetry, not a credential); the recipient routes look the
-- row up by token_hash only.
--
-- Existing rows keep working: their token_hash already is
-- sha256(<URL pre-image> || pepper). Each is given a fresh random token_id
-- (telemetry follows), so the stored row no longer contains the pre-image.
-- Like every data backfill in this chain, this UPDATE assumes the migration
-- role bypasses RLS (superuser / table owner with BYPASSRLS).
WITH remap AS MATERIALIZED (
    SELECT tenant_id, token_id AS old_id, gen_random_uuid() AS new_id
      FROM zt_share_tokens
), moved_telemetry AS (
    UPDATE zt_share_telemetry t
       SET token_id = r.new_id
      FROM remap r
     WHERE t.tenant_id = r.tenant_id AND t.token_id = r.old_id
)
UPDATE zt_share_tokens z
   SET token_id = r.new_id
  FROM remap r
 WHERE z.tenant_id = r.tenant_id AND z.token_id = r.old_id;

-- The hash is now the only lookup key: make it unique.
DROP INDEX IF EXISTS idx_zt_share_tokens_hash;
CREATE UNIQUE INDEX IF NOT EXISTS idx_zt_share_tokens_hash ON zt_share_tokens (token_hash);

-- Sanctioned pre-tenant lookup for the recipient routes (zt_share_tokens is
-- FORCE RLS; the tenant is learned from the row). Same design as
-- services/auth/migrations/000001_pretenant_lookup_functions.up.sql; role
-- from 000105. Returns only (tenant_id, token_id); everything else is read
-- inside database.WithTenantTx.
DO $$
BEGIN
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'dms_token_lookup') THEN
    CREATE ROLE dms_token_lookup NOLOGIN BYPASSRLS;
  ELSE
    ALTER ROLE dms_token_lookup NOLOGIN BYPASSRLS;
  END IF;
END $$;

GRANT SELECT ON zt_share_tokens TO dms_token_lookup;

CREATE OR REPLACE FUNCTION zt_share_lookup_by_token_hash(p_hash bytea)
RETURNS TABLE (tenant_id uuid, token_id uuid)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
  SELECT z.tenant_id, z.token_id
  FROM public.zt_share_tokens z
  WHERE z.token_hash = p_hash
$$;

ALTER FUNCTION zt_share_lookup_by_token_hash(bytea) OWNER TO dms_token_lookup;
REVOKE ALL ON FUNCTION zt_share_lookup_by_token_hash(bytea) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION zt_share_lookup_by_token_hash(bytea) TO dms_app;

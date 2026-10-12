-- M-26 — public share links under the production RLS role.
--
-- The anonymous share-link routes (GET /api/v1/shared/{token},
-- POST .../verify-password, GET .../download) learn the tenant FROM the
-- share_links row, so the token lookup necessarily runs before any tenant
-- context exists. share_links is FORCE ROW LEVEL SECURITY on
-- app.current_tenant, so under the NOBYPASSRLS dms_app role the lookup read
-- zero rows (fresh connection) or failed with 22P02 (pooled connection with
-- '' GUC residue): every public share link 404'd in production.
--
-- Sanctioned pre-tenant path (same design as
-- services/auth/migrations/000001_pretenant_lookup_functions.up.sql):
-- a SECURITY DEFINER exact-match function owned by a NOLOGIN BYPASSRLS role,
-- EXECUTE granted only to dms_app. It returns the MINIMUM needed to
-- continue — the owning tenant and the link id — and nothing else. The
-- service then reads the link itself inside database.WithTenantTx, under
-- the normal tenant-isolation policy.
--
-- dms_token_lookup is shared by the document service's public token
-- lookups (share links here; IRM licenses in 000109; zero-trust shares in
-- 000113). Nothing logs in as it; it only owns these fixed, parameterized,
-- exact-match functions. dms_app stays NOBYPASSRLS. No row_security=off.

DO $$
BEGIN
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'dms_token_lookup') THEN
    CREATE ROLE dms_token_lookup NOLOGIN BYPASSRLS;
  ELSE
    ALTER ROLE dms_token_lookup NOLOGIN BYPASSRLS;
  END IF;
END $$;

-- BYPASSRLS skips policies, not grants: the definer still needs SELECT.
GRANT SELECT ON share_links TO dms_token_lookup;

-- token_hash is sha256(token), UNIQUE across all tenants.
CREATE OR REPLACE FUNCTION share_link_lookup_by_token_hash(p_hash text)
RETURNS TABLE (tenant_id uuid, id uuid)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
  SELECT l.tenant_id, l.id
  FROM public.share_links l
  WHERE l.token_hash = p_hash
$$;

ALTER FUNCTION share_link_lookup_by_token_hash(text) OWNER TO dms_token_lookup;
REVOKE ALL ON FUNCTION share_link_lookup_by_token_hash(text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION share_link_lookup_by_token_hash(text) TO dms_app;

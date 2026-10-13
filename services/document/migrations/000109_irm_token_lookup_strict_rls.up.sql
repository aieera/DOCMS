-- IRM license lookup under the production RLS role; strict tenant policy.
--
-- Migration 000089 gave irm_containers and irm_licenses a USING clause that
-- admitted EVERY row whenever app.current_tenant was unset or '':
--
--     current_setting('app.current_tenant', true) IS NULL
--     OR current_setting('app.current_tenant', true) = ''
--     OR tenant_id::text = current_setting('app.current_tenant', true)
--
-- so the anonymous license-check callback could read by token hash on the
-- raw pool. That is fail-OPEN: any query that runs without a tenant context
-- (a raw-pool bug anywhere, or a pooled connection carrying the '' GUC
-- residue a committed WithTenantTx leaves behind) reads every tenant's
-- sealed-container coordinates and wrapped DEKs.
--
-- Replace both policies with the strict form every other tenant table uses,
-- and move the one legitimate pre-tenant read to a SECURITY DEFINER
-- exact-match function (pattern: services/auth/migrations/
-- 000001_pretenant_lookup_functions.up.sql; role from 000105). The function
-- returns only (tenant_id, license_id); the service reads the license and
-- container afterwards inside database.WithTenantTx.

DROP POLICY IF EXISTS irm_containers_tenant_isolation ON irm_containers;
CREATE POLICY irm_containers_tenant_isolation ON irm_containers
    USING (tenant_id::text = current_setting('app.current_tenant', true))
    WITH CHECK (tenant_id::text = current_setting('app.current_tenant', true));

DROP POLICY IF EXISTS irm_licenses_tenant_isolation ON irm_licenses;
CREATE POLICY irm_licenses_tenant_isolation ON irm_licenses
    USING (tenant_id::text = current_setting('app.current_tenant', true))
    WITH CHECK (tenant_id::text = current_setting('app.current_tenant', true));

DO $$
BEGIN
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'dms_token_lookup') THEN
    CREATE ROLE dms_token_lookup NOLOGIN BYPASSRLS;
  ELSE
    ALTER ROLE dms_token_lookup NOLOGIN BYPASSRLS;
  END IF;
END $$;

-- BYPASSRLS skips policies, not grants: the definer still needs SELECT.
GRANT SELECT ON irm_licenses TO dms_token_lookup;

-- token_hash is sha256(token), UNIQUE (idx_irm_licenses_token_hash).
CREATE OR REPLACE FUNCTION irm_lookup_license_by_token_hash(p_hash bytea)
RETURNS TABLE (tenant_id uuid, license_id uuid)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
  SELECT l.tenant_id, l.id
  FROM public.irm_licenses l
  WHERE l.token_hash = p_hash
$$;

ALTER FUNCTION irm_lookup_license_by_token_hash(bytea) OWNER TO dms_token_lookup;
REVOKE ALL ON FUNCTION irm_lookup_license_by_token_hash(bytea) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION irm_lookup_license_by_token_hash(bytea) TO dms_app;

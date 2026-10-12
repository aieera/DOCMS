DROP FUNCTION IF EXISTS irm_lookup_license_by_token_hash(bytea);
REVOKE SELECT ON irm_licenses FROM dms_token_lookup;

-- Restore the 000089 policies verbatim (fail-open for an empty tenant
-- context — the down path reinstates the old raw-pool lookup contract).
DROP POLICY IF EXISTS irm_containers_tenant_isolation ON irm_containers;
CREATE POLICY irm_containers_tenant_isolation ON irm_containers
    USING (
        current_setting('app.current_tenant', true) IS NULL
        OR current_setting('app.current_tenant', true) = ''
        OR tenant_id::text = current_setting('app.current_tenant', true)
    )
    WITH CHECK (tenant_id::text = current_setting('app.current_tenant', true));

DROP POLICY IF EXISTS irm_licenses_tenant_isolation ON irm_licenses;
CREATE POLICY irm_licenses_tenant_isolation ON irm_licenses
    USING (
        current_setting('app.current_tenant', true) IS NULL
        OR current_setting('app.current_tenant', true) = ''
        OR tenant_id::text = current_setting('app.current_tenant', true)
    )
    WITH CHECK (tenant_id::text = current_setting('app.current_tenant', true));

-- Wave 15.2 / geofencing.
--
-- Tenant-scoped per-scope policy rows. `scope_id` is nullable for
-- the tenant-level scope (which has no child id). `country_codes`
-- and `cidr_*` are optional — a policy may restrict by country only,
-- by CIDR only, or both. Enforcement semantics:
--   mode=allow:   request country/IP MUST be in the allow set
--   mode=deny:    request country/IP MUST NOT be in the deny set
--   mode=step_up: request matches deny/country ⇒ require re-MFA
--
-- `apply_to` is a scalar because the enforcement middleware already
-- knows the action it's gating; '*' means any.

BEGIN;

CREATE TABLE IF NOT EXISTS geofence_policies (
    tenant_id         UUID NOT NULL,
    id                UUID NOT NULL DEFAULT gen_random_uuid(),
    scope             TEXT NOT NULL
                          CHECK (scope IN ('tenant', 'workspace', 'document')),
    scope_id          UUID,
    mode              TEXT NOT NULL
                          CHECK (mode IN ('allow', 'deny', 'step_up')),
    country_codes     TEXT[],
    cidr_allowlist    CIDR[],
    cidr_denylist     CIDR[],
    apply_to          TEXT NOT NULL DEFAULT '*'
                          CHECK (apply_to IN ('read', 'write', 'admin', '*')),
    enabled           BOOLEAN NOT NULL DEFAULT true,
    created_by_user_id UUID,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id),
    -- tenant-scope rows have NULL scope_id; others must supply one.
    CHECK ((scope = 'tenant' AND scope_id IS NULL)
        OR (scope <> 'tenant' AND scope_id IS NOT NULL))
);

CREATE INDEX IF NOT EXISTS idx_geofence_policies_scope
    ON geofence_policies (tenant_id, scope, scope_id)
    WHERE enabled = true;

-- ISO 3166-1 alpha-2 sanity: reject anything that isn't a 2-char
-- upper-ASCII code. Array-level guard rather than per-row trigger so
-- admin CRUD fails fast at insert time.
CREATE OR REPLACE FUNCTION _geofence_validate_country_codes(codes TEXT[])
RETURNS BOOLEAN LANGUAGE plpgsql IMMUTABLE AS $$
BEGIN
    IF codes IS NULL THEN
        RETURN TRUE;
    END IF;
    FOR i IN 1..array_length(codes, 1) LOOP
        IF codes[i] !~ '^[A-Z]{2}$' THEN
            RETURN FALSE;
        END IF;
    END LOOP;
    RETURN TRUE;
END;
$$;

ALTER TABLE geofence_policies
    ADD CONSTRAINT geofence_country_codes_valid
    CHECK (_geofence_validate_country_codes(country_codes));

ALTER TABLE geofence_policies ENABLE ROW LEVEL SECURITY;
ALTER TABLE geofence_policies FORCE  ROW LEVEL SECURITY;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_policies
         WHERE tablename = 'geofence_policies'
           AND policyname = 'geofence_policies_tenant_isolation'
    ) THEN
        EXECUTE 'CREATE POLICY geofence_policies_tenant_isolation ON geofence_policies
                   USING (tenant_id = current_setting(''app.current_tenant'', true)::uuid)
                   WITH CHECK (tenant_id = current_setting(''app.current_tenant'', true)::uuid)';
    END IF;
END $$;

-- Audit updated_at on every change — shared function from the document
-- service's bulk schema.
CREATE TRIGGER trg_geofence_policies_updated_at
    BEFORE UPDATE ON geofence_policies FOR EACH ROW
    EXECUTE FUNCTION update_updated_at_column();

COMMIT;

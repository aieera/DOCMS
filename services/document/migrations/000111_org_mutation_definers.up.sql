-- Review remediation (M-27, DEC-2 pattern) — sanctioned organizations writes.
--
-- organizations carries no RLS (it IS the tenant registry), but 000001 does
-- `REVOKE INSERT, UPDATE, DELETE ON organizations FROM dms_app`: the app
-- role may read the registry, never rewrite it. Every runtime write path —
-- auth's owner-only region-pin endpoint, billing's provisioning, suspension
-- and feature-flag sync — therefore fails under the production role. The
-- writes go through these fixed, parameterised SECURITY DEFINER functions
-- instead of a broad table grant: dms_app can create an organization or
-- change exactly one of region / status / feature flags, and nothing else
-- (no slug/name rewrite, no delete, no arbitrary settings keys).
--
-- Same shape as services/auth/migrations/000001_pretenant_lookup_functions:
-- a dedicated NOLOGIN definer role holding the minimum table privilege,
-- `SET search_path = pg_catalog, public`, REVOKE ALL FROM PUBLIC, EXECUTE
-- granted only to dms_app. The definer role needs no BYPASSRLS because
-- organizations has no RLS policy; it gets SELECT/INSERT/UPDATE on
-- organizations only (no DELETE).
--
-- Status and feature flags live in organizations.settings (keys `status`
-- and `features`); there is no dedicated column. Region pin is the tenant
-- default region, organizations.primary_region.

DO $$
BEGIN
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'dms_org_writer') THEN
    CREATE ROLE dms_org_writer NOLOGIN NOBYPASSRLS;
  ELSE
    ALTER ROLE dms_org_writer NOLOGIN NOBYPASSRLS;
  END IF;
END $$;

GRANT SELECT, INSERT, UPDATE ON organizations TO dms_org_writer;

-- org_create — provision a tenant. p_id NULL → gen_random_uuid().
-- Returns the organization id. Fails (unique_violation) on a taken slug.
CREATE OR REPLACE FUNCTION org_create(
  p_id uuid, p_name text, p_slug text, p_plan text,
  p_primary_region text, p_settings jsonb
)
RETURNS uuid
LANGUAGE plpgsql
VOLATILE
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
DECLARE
  v_id uuid;
BEGIN
  IF p_name IS NULL OR btrim(p_name) = '' THEN
    RAISE EXCEPTION 'org_create: name required' USING ERRCODE = '22023';
  END IF;
  IF p_slug IS NULL OR btrim(p_slug) = '' THEN
    RAISE EXCEPTION 'org_create: slug required' USING ERRCODE = '22023';
  END IF;
  INSERT INTO public.organizations (id, name, slug, plan, primary_region, settings)
  VALUES (COALESCE(p_id, gen_random_uuid()),
          btrim(p_name),
          lower(btrim(p_slug)),
          COALESCE(NULLIF(btrim(p_plan), ''), 'standard'),
          COALESCE(NULLIF(btrim(p_primary_region), ''), 'us-east-1'),
          COALESCE(p_settings, '{}'::jsonb))
  RETURNING id INTO v_id;
  RETURN v_id;
END
$$;

-- org_set_region_pin — change the tenant default region (new uploads land
-- there; existing documents keep their own region_pin). Returns false when
-- no live organization has that id.
CREATE OR REPLACE FUNCTION org_set_region_pin(p_org uuid, p_region text)
RETURNS boolean
LANGUAGE plpgsql
VOLATILE
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
BEGIN
  IF p_region IS NULL OR btrim(p_region) = '' THEN
    RAISE EXCEPTION 'org_set_region_pin: region required' USING ERRCODE = '22023';
  END IF;
  UPDATE public.organizations
     SET primary_region = btrim(p_region), updated_at = now()
   WHERE id = p_org AND deleted_at IS NULL;
  RETURN FOUND;
END
$$;

-- org_set_status — record the tenant's commercial status in
-- settings.status. One of active | suspended | cancelled. Returns false
-- when no live organization has that id.
CREATE OR REPLACE FUNCTION org_set_status(p_org uuid, p_status text)
RETURNS boolean
LANGUAGE plpgsql
VOLATILE
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
BEGIN
  IF p_status IS NULL OR p_status NOT IN ('active', 'suspended', 'cancelled') THEN
    RAISE EXCEPTION 'org_set_status: status must be active, suspended or cancelled'
      USING ERRCODE = '22023';
  END IF;
  UPDATE public.organizations
     SET settings = jsonb_set(COALESCE(settings, '{}'::jsonb), '{status}', to_jsonb(p_status)),
         updated_at = now()
   WHERE id = p_org AND deleted_at IS NULL;
  RETURN FOUND;
END
$$;

-- org_set_feature_flags — replace settings.features with p_flags (a JSON
-- object). Returns false when no live organization has that id.
CREATE OR REPLACE FUNCTION org_set_feature_flags(p_org uuid, p_flags jsonb)
RETURNS boolean
LANGUAGE plpgsql
VOLATILE
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
BEGIN
  IF p_flags IS NULL OR jsonb_typeof(p_flags) <> 'object' THEN
    RAISE EXCEPTION 'org_set_feature_flags: flags must be a JSON object' USING ERRCODE = '22023';
  END IF;
  UPDATE public.organizations
     SET settings = jsonb_set(COALESCE(settings, '{}'::jsonb), '{features}', p_flags),
         updated_at = now()
   WHERE id = p_org AND deleted_at IS NULL;
  RETURN FOUND;
END
$$;

ALTER FUNCTION org_create(uuid, text, text, text, text, jsonb) OWNER TO dms_org_writer;
ALTER FUNCTION org_set_region_pin(uuid, text)                   OWNER TO dms_org_writer;
ALTER FUNCTION org_set_status(uuid, text)                       OWNER TO dms_org_writer;
ALTER FUNCTION org_set_feature_flags(uuid, jsonb)               OWNER TO dms_org_writer;

REVOKE ALL ON FUNCTION org_create(uuid, text, text, text, text, jsonb) FROM PUBLIC;
REVOKE ALL ON FUNCTION org_set_region_pin(uuid, text)                   FROM PUBLIC;
REVOKE ALL ON FUNCTION org_set_status(uuid, text)                       FROM PUBLIC;
REVOKE ALL ON FUNCTION org_set_feature_flags(uuid, jsonb)               FROM PUBLIC;

GRANT EXECUTE ON FUNCTION org_create(uuid, text, text, text, text, jsonb) TO dms_app;
GRANT EXECUTE ON FUNCTION org_set_region_pin(uuid, text)                   TO dms_app;
GRANT EXECUTE ON FUNCTION org_set_status(uuid, text)                       TO dms_app;
GRANT EXECUTE ON FUNCTION org_set_feature_flags(uuid, jsonb)               TO dms_app;

DROP FUNCTION IF EXISTS org_set_feature_flags(uuid, jsonb);
DROP FUNCTION IF EXISTS org_set_status(uuid, text);
DROP FUNCTION IF EXISTS org_set_region_pin(uuid, text);
DROP FUNCTION IF EXISTS org_create(uuid, text, text, text, text, jsonb);

DO $$
BEGIN
  IF EXISTS (SELECT FROM pg_roles WHERE rolname = 'dms_org_writer') THEN
    REVOKE ALL ON organizations FROM dms_org_writer;
    DROP ROLE dms_org_writer;
  END IF;
END $$;

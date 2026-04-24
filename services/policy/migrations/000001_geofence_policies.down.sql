BEGIN;

DROP TRIGGER IF EXISTS trg_geofence_policies_updated_at ON geofence_policies;
DROP TABLE IF EXISTS geofence_policies;
DROP FUNCTION IF EXISTS _geofence_validate_country_codes(TEXT[]);

COMMIT;

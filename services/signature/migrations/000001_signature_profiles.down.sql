BEGIN;
DROP TRIGGER IF EXISTS trg_signature_profiles_updated_at ON signature_profiles;
DROP TABLE IF EXISTS signature_profiles;
COMMIT;

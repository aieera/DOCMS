BEGIN;
DROP INDEX IF EXISTS uq_intake_drop_folders_host_path;
-- The tenant FK exists only when 000002 ran after the document chain.
DO $$
BEGIN
    IF to_regclass('public.connector_oauth_states') IS NOT NULL THEN
        ALTER TABLE connector_oauth_states DROP CONSTRAINT IF EXISTS connector_oauth_states_tenant_fk;
    END IF;
END $$;
DROP TABLE IF EXISTS connector_oauth_states;
COMMIT;

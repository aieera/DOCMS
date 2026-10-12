BEGIN;
DROP INDEX IF EXISTS uq_intake_drop_folders_host_path;
DROP TABLE IF EXISTS connector_oauth_states;
COMMIT;

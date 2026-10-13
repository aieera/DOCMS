-- SeDoc — application-role bootstrap for NON-dev databases (O-6 / DEC-1).
--
-- Run by scripts/migrate-all.sh BEFORE the migrations when
-- SEDOC_DB_BOOTSTRAP_ROLES=1 (the Helm migrate Job and the prod compose
-- `migrate` service do this), as the migration/owner role. Idempotent.
--
--   * creates dms_app / dms_readonly if missing (NOLOGIN, NOBYPASSRLS — the
--     document migrations create them the same way on a fresh database);
--   * grants dms_app LOGIN with the password from $DMS_APP_PASSWORD, and
--     dms_readonly LOGIN only when $DMS_READONLY_PASSWORD is set (else it
--     stays NOLOGIN);
--   * sets the default privileges every later migration relies on, so a
--     table created by the migration role is usable by dms_app (the dev
--     stack gets the same from scripts/init-db.sql).
--
-- Passwords are read from the environment with \getenv (psql 15+), never
-- from the command line, so they do not show up in the process list.
-- The migration role must be able to CREATE ROLE ... BYPASSRLS later in the
-- chain (dms_auth_lookup, dms_outbox_drain, dms_audit_admin): a superuser.

\set ON_ERROR_STOP on

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'dms_app') THEN
        CREATE ROLE dms_app NOLOGIN NOBYPASSRLS;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'dms_readonly') THEN
        CREATE ROLE dms_readonly NOLOGIN NOBYPASSRLS;
    END IF;
END
$$;

\getenv dms_app_password DMS_APP_PASSWORD
\if :{?dms_app_password}
ALTER ROLE dms_app LOGIN PASSWORD :'dms_app_password' NOBYPASSRLS;
\else
DO $$ BEGIN RAISE EXCEPTION 'db-bootstrap-roles: DMS_APP_PASSWORD is not set'; END $$;
\endif

\getenv dms_readonly_password DMS_READONLY_PASSWORD
\if :{?dms_readonly_password}
ALTER ROLE dms_readonly LOGIN PASSWORD :'dms_readonly_password' NOBYPASSRLS;
\else
ALTER ROLE dms_readonly NOLOGIN NOBYPASSRLS;
\endif

SELECT format('GRANT CONNECT ON DATABASE %I TO dms_app, dms_readonly', current_database()) \gexec
GRANT USAGE ON SCHEMA public TO dms_app, dms_readonly;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO dms_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT USAGE, SELECT ON SEQUENCES TO dms_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT ON TABLES TO dms_readonly;

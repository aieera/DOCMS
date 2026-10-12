-- SeDoc — initial database bootstrap.
-- Runs once on first Postgres container start (docker-entrypoint-initdb.d).
-- Service-specific schema is applied via golang-migrate in each service's
-- migrations/ directory.

CREATE EXTENSION IF NOT EXISTS "uuid-ossp";
CREATE EXTENSION IF NOT EXISTS "pgcrypto";
CREATE EXTENSION IF NOT EXISTS "ltree";
CREATE EXTENSION IF NOT EXISTS "btree_gin";
CREATE EXTENSION IF NOT EXISTS "pg_trgm";

-- Application roles ---------------------------------------------------------
-- DEV ONLY. This file runs from docker-entrypoint-initdb.d on the DEV compose
-- stack's first boot, before any migration. It is the only place a password
-- is hard-coded, and only the dev password.
--
-- dms_app:      used by services for day-to-day queries. NOBYPASSRLS.
-- dms_readonly: used by analytics / support tooling. SELECT only.
--
-- O-6: the document migrations create both roles NOLOGIN with no password.
-- Every deployment path grants LOGIN + a password from its own secrets:
--   dev compose        this file (devpassword)
--   prod compose       the one-shot `migrate` service (DMS_APP_PASSWORD)
--   Helm               the migrate Job's role bootstrap (sedoc-db-credentials)
--   Ansible / EC2      the postgres role task (vault)
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'dms_app') THEN
        CREATE ROLE dms_app LOGIN PASSWORD 'devpassword' NOBYPASSRLS;
    ELSE
        ALTER ROLE dms_app LOGIN PASSWORD 'devpassword' NOBYPASSRLS;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'dms_readonly') THEN
        CREATE ROLE dms_readonly LOGIN PASSWORD 'devpassword' NOBYPASSRLS;
    ELSE
        ALTER ROLE dms_readonly LOGIN PASSWORD 'devpassword' NOBYPASSRLS;
    END IF;
    -- current_database(), not a literal: the entrypoint runs this file in
    -- POSTGRES_DB, whatever that is named.
    EXECUTE format('GRANT CONNECT ON DATABASE %I TO dms_app, dms_readonly', current_database());
END
$$;

GRANT USAGE ON SCHEMA public TO dms_app, dms_readonly;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO dms_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT USAGE, SELECT ON SEQUENCES TO dms_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT ON TABLES TO dms_readonly;

-- Row-level security session variable --------------------------------------
-- Policies use: current_setting('app.current_tenant', true)::uuid
-- The `true` argument returns NULL (not an error) when the GUC is unset,
-- which makes the policy match zero rows — the desired safe default for a
-- connection that forgot to set the tenant.
--
-- DO NOT set a database-level default (ALTER DATABASE ... SET) here: an
-- empty-string default breaks the NULL-safe path because ''::uuid raises an
-- `invalid input syntax for uuid` error instead of returning no rows.
-- Leave the GUC truly unset; pkg/middleware/tenant.go sets it per request.

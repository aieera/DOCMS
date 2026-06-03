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
-- dms_app:      used by services for day-to-day queries. No superuser.
-- dms_readonly: used by analytics / support tooling. SELECT only.
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'dms_app') THEN
        CREATE ROLE dms_app LOGIN PASSWORD 'devpassword';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'dms_readonly') THEN
        CREATE ROLE dms_readonly LOGIN PASSWORD 'devpassword';
    END IF;
END
$$;

GRANT CONNECT ON DATABASE vaultdms TO dms_app, dms_readonly;
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

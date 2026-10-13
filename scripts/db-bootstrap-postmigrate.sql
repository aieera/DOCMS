-- SeDoc — least-privilege operational roles (DEC-1 / H-19), run by
-- scripts/migrate-all.sh AFTER every track when SEDOC_DB_BOOTSTRAP_ROLES=1,
-- as the migration/owner role. Idempotent; converges grants on every run.
-- It lives here, not in a schema migration, because the grants name tables
-- the migrations create and because an edit to an already-applied migration
-- would never reach an existing database.
--
--   dms_evidence  SOC 2 evidence CronJob (scripts/soc2-evidence.sh). The
--                 bundle is cross-tenant by design (admin roster + audit
--                 counts per tenant), so BYPASSRLS — but SELECT on exactly
--                 the columns the script reads, nothing else: no DDL, no
--                 writes, no other tables.
--   dms_backup    pg_dump CronJob. Reads everything (pg_read_all_data) and
--                 bypasses RLS so the dump is complete; cannot write.
--
-- Neither is a superuser or can create roles. LOGIN + password come from
-- DMS_EVIDENCE_PASSWORD / DMS_BACKUP_PASSWORD (\getenv, never argv); a role
-- whose password is unset stays NOLOGIN.

\set ON_ERROR_STOP on

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'dms_evidence') THEN
        CREATE ROLE dms_evidence NOLOGIN;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'dms_backup') THEN
        CREATE ROLE dms_backup NOLOGIN;
    END IF;
END
$$;
ALTER ROLE dms_evidence NOSUPERUSER NOCREATEROLE NOCREATEDB NOREPLICATION BYPASSRLS;
ALTER ROLE dms_backup   NOSUPERUSER NOCREATEROLE NOCREATEDB NOREPLICATION BYPASSRLS;

SELECT format('GRANT CONNECT ON DATABASE %I TO dms_evidence, dms_backup', current_database()) \gexec
GRANT USAGE ON SCHEMA public TO dms_evidence, dms_backup;

-- dms_evidence: column grants for the two queries in soc2-evidence.sh.
REVOKE ALL ON ALL TABLES IN SCHEMA public FROM dms_evidence;
GRANT SELECT (tenant_id, id, email, role, status, last_login_at) ON users TO dms_evidence;
GRANT SELECT (tenant_id, created_at) ON audit_events TO dms_evidence;

-- dms_backup: read-only over the whole database.
GRANT pg_read_all_data TO dms_backup;

\getenv dms_evidence_password DMS_EVIDENCE_PASSWORD
\if :{?dms_evidence_password}
ALTER ROLE dms_evidence LOGIN PASSWORD :'dms_evidence_password';
\else
ALTER ROLE dms_evidence NOLOGIN;
\endif

\getenv dms_backup_password DMS_BACKUP_PASSWORD
\if :{?dms_backup_password}
ALTER ROLE dms_backup LOGIN PASSWORD :'dms_backup_password';
\else
ALTER ROLE dms_backup NOLOGIN;
\endif

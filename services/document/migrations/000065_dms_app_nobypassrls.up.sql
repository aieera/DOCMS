-- FIX-9 (audit C6 + Section 14) — make NOBYPASSRLS the schema-of-
-- record invariant for the app DB role, not an Ansible-only
-- contract.
--
-- The original 000001 created `dms_app` LOGIN ... but never set
-- BYPASSRLS / NOBYPASSRLS explicitly. Postgres defaults to NOBYPASSRLS,
-- so on a fresh install the role IS NOBYPASSRLS. But the audit
-- correctly flagged that this is configuration-managed, not
-- schema-enforced: an Ansible drift, a manual GRANT, or a
-- consultant's "let me just elevate for a sec" turns the invariant
-- into a silent vulnerability — every RLS policy becomes advisory.
--
-- This migration:
--   1. Idempotently CREATEs the role if it's missing (covers
--      environments where 000001 was edited or skipped).
--   2. ALTERs it to NOBYPASSRLS unconditionally so future drift gets
--      corrected on the next migrate-up.
--   3. Same for dms_readonly (analogous risk).
--
-- The startup assertion in pkg/database.AssertRLSPosture is the
-- runtime backstop; this migration is the schema-of-record half.

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'dms_app') THEN
        CREATE ROLE dms_app LOGIN PASSWORD 'devpassword';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'dms_readonly') THEN
        CREATE ROLE dms_readonly LOGIN PASSWORD 'devpassword';
    END IF;
END $$;

ALTER ROLE dms_app      NOBYPASSRLS;
ALTER ROLE dms_readonly NOBYPASSRLS;

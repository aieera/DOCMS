-- Reverse of 000006.
DROP FUNCTION IF EXISTS audit_set_event_hash(uuid, uuid, text, text);
DROP FUNCTION IF EXISTS audit_redact_subject(uuid, text);

DO $do$
BEGIN
    IF EXISTS (SELECT FROM pg_roles WHERE rolname = 'dms_audit_admin') THEN
        EXECUTE 'REVOKE ALL ON public.audit_events FROM dms_audit_admin';
        EXECUTE 'REVOKE USAGE ON SCHEMA public FROM dms_audit_admin';
        -- The role is cluster-wide; another database may still use it, so
        -- it is left in place (it is NOLOGIN and now owns nothing here).
    END IF;
END
$do$;

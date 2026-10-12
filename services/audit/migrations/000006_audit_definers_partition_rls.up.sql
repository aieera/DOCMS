-- H-12 — a working GDPR erasure path for the WORM audit trail.
--
-- AnonymizeSubject could never succeed: it wrote ip_address = '' into an
-- INET column (a parse error in every environment) and it UPDATEd
-- audit_events, which document migration 000001 revokes from dms_app
-- (append-only). The re-chain that has to follow a redaction failed the
-- same way. There was no sanctioned path at all.
--
-- Two SECURITY DEFINER functions are that path. They follow the shape of
-- services/auth/migrations/000001 with one deliberate difference: the
-- owner, dms_audit_admin, is NOLOGIN but NOT BYPASSRLS. The tenant is
-- known when an erasure runs, so the functions run under the normal
-- audit_events tenant policies, and each one also refuses to act unless
-- app.current_tenant (set by database.WithTenantTx) names the tenant it
-- was asked to change. dms_audit_admin holds UPDATE only on the columns
-- the two functions write; dms_app gets EXECUTE and still cannot UPDATE
-- audit_events itself.
--
--   audit_redact_subject(tenant, subject) — replaces the subject's actor
--     fields with 'REDACTED', clears actor_id, ip_address and user_agent,
--     and drops the actor keys the outbox publisher stamps into the stored
--     envelope. Returns the ids it changed.
--   audit_set_event_hash(tenant, id, previous_hash, event_hash) — writes
--     one re-computed link of the chain. Only v2 hashes are accepted.
--
-- The service runs both inside one tenant transaction that holds the
-- chain's advisory lock, then signs a fresh checkpoint over the new head,
-- so a re-chained trail is still third-party verifiable.

DO $do$
BEGIN
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'dms_audit_admin') THEN
        CREATE ROLE dms_audit_admin NOLOGIN NOBYPASSRLS;
    ELSE
        ALTER ROLE dms_audit_admin NOLOGIN NOBYPASSRLS;
    END IF;
END
$do$;

GRANT USAGE ON SCHEMA public TO dms_audit_admin;
GRANT SELECT ON public.audit_events TO dms_audit_admin;
GRANT UPDATE (actor, actor_id, actor_name, ip_address, user_agent, details,
              previous_hash, event_hash)
    ON public.audit_events TO dms_audit_admin;

CREATE OR REPLACE FUNCTION audit_redact_subject(p_tenant uuid, p_subject text)
RETURNS SETOF uuid
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $fn$
BEGIN
    IF p_tenant IS NULL OR COALESCE(p_subject, '') IN ('', 'REDACTED') THEN
        RAISE EXCEPTION 'audit_redact_subject: tenant and subject are required'
            USING ERRCODE = '22023';
    END IF;
    IF NULLIF(current_setting('app.current_tenant', true), '')::uuid IS DISTINCT FROM p_tenant THEN
        RAISE EXCEPTION 'audit_redact_subject: caller is not in tenant %', p_tenant
            USING ERRCODE = '42501';
    END IF;

    RETURN QUERY
    UPDATE public.audit_events
       SET actor      = 'REDACTED',
           actor_id   = NULL,
           actor_name = 'REDACTED',
           ip_address = NULL,
           user_agent = NULL,
           details    = details
                        - 'vdmsactorid' - 'vdmsactorname' - 'vdmsclientip' - 'vdmsuseragent'
                        #- '{data,actor_id}' #- '{data,actor_name}'
                        #- '{data,ip_address}' #- '{data,user_agent}'
     WHERE tenant_id = p_tenant
       AND actor = p_subject
    RETURNING id;
END
$fn$;

CREATE OR REPLACE FUNCTION audit_set_event_hash(p_tenant uuid, p_id uuid, p_prev text, p_hash text)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $fn$
DECLARE
    v_n integer;
BEGIN
    IF NULLIF(current_setting('app.current_tenant', true), '')::uuid IS DISTINCT FROM p_tenant THEN
        RAISE EXCEPTION 'audit_set_event_hash: caller is not in tenant %', p_tenant
            USING ERRCODE = '42501';
    END IF;
    IF p_id IS NULL OR p_prev IS NULL OR p_hash IS NULL OR p_hash NOT LIKE 'v2:%' THEN
        RAISE EXCEPTION 'audit_set_event_hash: id, previous hash and a v2 hash are required'
            USING ERRCODE = '22023';
    END IF;

    UPDATE public.audit_events
       SET previous_hash = p_prev,
           event_hash    = p_hash
     WHERE tenant_id = p_tenant
       AND id = p_id;
    GET DIAGNOSTICS v_n = ROW_COUNT;
    IF v_n <> 1 THEN
        RAISE EXCEPTION 'audit_set_event_hash: expected 1 event %, matched %', p_id, v_n
            USING ERRCODE = 'P0002';
    END IF;
END
$fn$;

ALTER FUNCTION audit_redact_subject(uuid, text)             OWNER TO dms_audit_admin;
ALTER FUNCTION audit_set_event_hash(uuid, uuid, text, text) OWNER TO dms_audit_admin;

REVOKE ALL ON FUNCTION audit_redact_subject(uuid, text)             FROM PUBLIC;
REVOKE ALL ON FUNCTION audit_set_event_hash(uuid, uuid, text, text) FROM PUBLIC;

DO $do$
BEGIN
    IF EXISTS (SELECT FROM pg_roles WHERE rolname = 'dms_app') THEN
        EXECUTE 'GRANT EXECUTE ON FUNCTION audit_redact_subject(uuid, text) TO dms_app';
        EXECUTE 'GRANT EXECUTE ON FUNCTION audit_set_event_hash(uuid, uuid, text, text) TO dms_app';
    END IF;
END
$do$;

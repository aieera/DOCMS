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

-- ===========================================================================
-- L-10 — audit_events partitions: RLS on every partition, no direct grants.
--
-- Queries routed through audit_events check privileges and RLS on the
-- parent only, which is where the tenant policies and the WORM revoke
-- live. A partition queried BY NAME is checked on its own, and every
-- partition had no RLS plus direct grants: document 000001's blanket
-- GRANT gave dms_app SELECT/INSERT/UPDATE on audit_events_2026_04..07,
-- and the init-db default privileges give dms_app SELECT/INSERT/UPDATE/
-- DELETE on every partition the maintainer creates. That was a
-- cross-tenant read and an UPDATE/DELETE path around the append-only
-- trail.
--
-- Every partition now has ENABLE + FORCE ROW LEVEL SECURITY and no grants
-- for dms_app, dms_readonly or PUBLIC. The only policy on a partition is
-- audit_partition_owner_maintenance, which admits the owner of
-- audit_events and nobody else: FORCE subjects the owner to RLS, and the
-- maintenance functions (DEFAULT→month relocation, the DEFAULT-rows
-- gauge) read partitions directly as that owner. dms_app has no grant on a
-- partition, so for it the policy is moot.
-- ===========================================================================

CREATE OR REPLACE FUNCTION audit_secure_partition(p_part regclass)
RETURNS void
LANGUAGE plpgsql
SET search_path = pg_catalog, public
AS $fn$
DECLARE
    r record;
BEGIN
    EXECUTE format('REVOKE ALL ON %s FROM PUBLIC', p_part);
    FOR r IN SELECT rolname FROM pg_roles WHERE rolname IN ('dms_app', 'dms_readonly', 'dms_audit_admin') LOOP
        EXECUTE format('REVOKE ALL ON %s FROM %I', p_part, r.rolname);
    END LOOP;
    EXECUTE format('ALTER TABLE %s ENABLE ROW LEVEL SECURITY', p_part);
    EXECUTE format('ALTER TABLE %s FORCE ROW LEVEL SECURITY', p_part);
    IF NOT EXISTS (SELECT 1 FROM pg_policy
                    WHERE polrelid = p_part AND polname = 'audit_partition_owner_maintenance') THEN
        EXECUTE format($sql$
            CREATE POLICY audit_partition_owner_maintenance ON %s
                USING (current_user = (SELECT pg_get_userbyid(relowner) FROM pg_class
                                        WHERE oid = 'public.audit_events'::regclass))
                WITH CHECK (current_user = (SELECT pg_get_userbyid(relowner) FROM pg_class
                                             WHERE oid = 'public.audit_events'::regclass))
        $sql$, p_part);
    END IF;
END
$fn$;
REVOKE ALL ON FUNCTION audit_secure_partition(regclass) FROM PUBLIC;

-- Existing partitions: document 000001's four months, DEFAULT, and every
-- month audit 000005 / the service maintainer has created.
DO $do$
DECLARE
    r record;
BEGIN
    FOR r IN SELECT i.inhrelid::regclass AS part
               FROM pg_inherits i
              WHERE i.inhparent = 'public.audit_events'::regclass
    LOOP
        PERFORM audit_secure_partition(r.part);
    END LOOP;
END
$do$;

-- Partitions created from now on: same body as audit 000005, plus
-- audit_secure_partition on the new partition after ATTACH (instead of the
-- direct grants 000005 handed out). The relocation INSERT runs before the
-- new table has RLS; the DELETE from DEFAULT is admitted by the owner
-- policy above.
CREATE OR REPLACE FUNCTION ensure_audit_partition_range(p_from date, p_to date)
RETURNS integer
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $fn$
DECLARE
    v_month       date;
    v_last        date;
    v_lo          timestamptz;
    v_hi          timestamptz;
    v_part        text;
    v_created     integer := 0;
    v_moved       bigint;
    v_has_default boolean;
BEGIN
    IF p_from IS NULL OR p_to IS NULL THEN
        RAISE EXCEPTION 'ensure_audit_partition_range: bounds must not be null';
    END IF;

    v_month := date_trunc('month', p_from)::date;
    v_last  := date_trunc('month', p_to)::date;
    IF v_last > (v_month + interval '10 years')::date THEN
        v_last := (v_month + interval '10 years')::date;
    END IF;

    v_has_default := to_regclass('public.audit_events_default') IS NOT NULL;

    WHILE v_month <= v_last LOOP
        v_part := 'audit_events_' || to_char(v_month, 'YYYY_MM');
        v_lo := v_month::timestamp AT TIME ZONE 'UTC';
        v_hi := (v_month + interval '1 month')::timestamp AT TIME ZONE 'UTC';

        IF to_regclass('public.' || quote_ident(v_part)) IS NULL THEN
            EXECUTE format(
                'CREATE TABLE public.%I (LIKE public.audit_events INCLUDING ALL)', v_part);

            IF v_has_default THEN
                EXECUTE format($sql$
                    WITH moved AS (
                        DELETE FROM ONLY public.audit_events_default
                         WHERE created_at >= %L AND created_at < %L
                        RETURNING *
                    )
                    INSERT INTO public.%I SELECT * FROM moved
                $sql$, v_lo, v_hi, v_part);
                GET DIAGNOSTICS v_moved = ROW_COUNT;
                IF v_moved > 0 THEN
                    RAISE NOTICE 'ensure_audit_partition_range: relocated % row(s) from audit_events_default into %',
                        v_moved, v_part;
                END IF;
            END IF;

            EXECUTE format(
                'ALTER TABLE public.audit_events ATTACH PARTITION public.%I FOR VALUES FROM (%L) TO (%L)',
                v_part, v_lo, v_hi);

            PERFORM audit_secure_partition(format('public.%I', v_part)::regclass);

            v_created := v_created + 1;
        END IF;

        v_month := (v_month + interval '1 month')::date;
    END LOOP;

    RETURN v_created;
END
$fn$;
REVOKE ALL ON FUNCTION ensure_audit_partition_range(date, date) FROM PUBLIC;

-- Checkpoints are append-only (audit 000003 revoked UPDATE/DELETE from
-- PUBLIC only; the init-db default privileges still gave dms_app both).
DO $do$
BEGIN
    IF EXISTS (SELECT FROM pg_roles WHERE rolname = 'dms_app') THEN
        EXECUTE 'REVOKE UPDATE, DELETE, TRUNCATE ON public.audit_checkpoints FROM dms_app';
    END IF;
END
$do$;

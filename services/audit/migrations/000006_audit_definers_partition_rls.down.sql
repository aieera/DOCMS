-- Reverse of 000006.
--
-- Partition hardening is left in place: dropping RLS and restoring direct
-- grants on audit evidence would re-open the cross-tenant / WORM bypass,
-- and it is harmless to the 000005 functions restored below.

-- Restore the 000005 maintenance body (direct grants instead of
-- audit_secure_partition) so the function no longer depends on the helper.
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
            EXECUTE format('CREATE TABLE public.%I (LIKE public.audit_events INCLUDING ALL)', v_part);
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
            END IF;
            EXECUTE format(
                'ALTER TABLE public.audit_events ATTACH PARTITION public.%I FOR VALUES FROM (%L) TO (%L)',
                v_part, v_lo, v_hi);
            IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'dms_app') THEN
                EXECUTE format('GRANT SELECT, INSERT ON public.%I TO dms_app', v_part);
            END IF;
            IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'dms_readonly') THEN
                EXECUTE format('GRANT SELECT ON public.%I TO dms_readonly', v_part);
            END IF;
            v_created := v_created + 1;
        END IF;
        v_month := (v_month + interval '1 month')::date;
    END LOOP;
    RETURN v_created;
END
$fn$;
REVOKE ALL ON FUNCTION ensure_audit_partition_range(date, date) FROM PUBLIC;
DROP FUNCTION IF EXISTS audit_secure_partition(regclass);

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

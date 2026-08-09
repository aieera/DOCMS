-- Reverse of 000005.
--
-- The functions go unconditionally. The partitions go only when EMPTY: a
-- schema rollback must never destroy audit evidence, and dropping a
-- populated audit_events partition would do exactly that. On a fresh
-- database (test lanes, CI) everything is empty, so the rollback is
-- complete; on a live database a populated partition is kept and a
-- WARNING is raised naming it.
DROP FUNCTION IF EXISTS ensure_audit_partitions(integer);
DROP FUNCTION IF EXISTS ensure_audit_partition_range(date, date);
DROP FUNCTION IF EXISTS audit_default_partition_rows();

DO $do$
DECLARE
    r record;
    n bigint;
BEGIN
    FOR r IN
        SELECT c.relname AS name
        FROM pg_inherits i
        JOIN pg_class c ON c.oid = i.inhrelid
        WHERE i.inhparent = 'public.audit_events'::regclass
          -- Everything this migration could have created: the DEFAULT
          -- partition plus every month from 2026-08 forward. The four
          -- partitions document migration 000001 created (2026_04 ..
          -- 2026_07) sort below this bound and are left alone.
          AND c.relname >= 'audit_events_2026_08'
        ORDER BY c.relname
    LOOP
        EXECUTE format('SELECT count(*) FROM ONLY public.%I', r.name) INTO n;
        IF n = 0 THEN
            EXECUTE format('DROP TABLE public.%I', r.name);
        ELSE
            RAISE WARNING 'audit 000005 down: keeping partition % (% row(s)) — refusing to destroy audit evidence', r.name, n;
        END IF;
    END LOOP;
END
$do$;

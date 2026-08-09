-- BUG-01 — audit_events silently rejected EVERY insert.
--
-- audit_events is PARTITION BY RANGE (created_at) (document migration
-- 000001, "TABLE 19"). That migration created exactly four monthly
-- partitions — 2026_04 .. 2026_07, the last ending 2026-08-01 — with the
-- comment "Production operators roll new partitions forward via a cron
-- job". No such job exists anywhere in this repo. From 2026-08-01 onward
-- every insert failed with
--
--     ERROR: no partition of relation "audit_events" found for row
--     (SQLSTATE 23514)
--
-- so the audit log was empty and the entire compliance stack built on it
-- (legal hold, e-discovery, SIEM forwarding, GDPR/DSR, retention) had no
-- data. The NATS consumer NAK'd and the messages died after MaxDeliver.
--
-- This migration makes the failure mode impossible rather than merely
-- postponing it:
--
--   1. A DEFAULT partition. For an audit table, a row landing in a
--      suboptimal partition is infinitely better than a dropped row, so
--      the tuple-routing "no partition found" error can never be raised
--      again. Adding a DEFAULT to an already-partitioned table is legal
--      when no existing row would belong to it — nothing outside
--      2026-04..2026-07 exists, so this is a metadata-only change.
--
--   2. ensure_audit_partition_range() / ensure_audit_partitions(): the
--      idempotent maintenance the missing cron job was supposed to do.
--      The audit service calls ensure_audit_partitions() at startup and
--      on a daily ticker (services/audit/internal/service/partitions.go),
--      so the schedule now lives in the product, not in an operator
--      runbook. Rows that already landed in DEFAULT for a month are
--      relocated into that month's partition when it is created.
--
--   3. Partitions for 2026-08 .. 2027-12, so a database migrating today
--      heals the moment this runs, before any Go code redeploys.
--
-- SECURITY NOTE. The maintenance functions are SECURITY DEFINER because
-- the app role (dms_app, NOBYPASSRLS, holding only SELECT+INSERT on
-- audit_events) cannot execute DDL or relocate rows out of DEFAULT. The
-- escalation surface is kept minimal the same way auth's pre-tenant
-- lookups do it (services/auth/migrations/000001):
--   * search_path is pinned to pg_catalog, public;
--   * every identifier in dynamic SQL is computed internally from a date
--     and passed through format(%I) — no caller string reaches the SQL;
--   * the only caller-supplied value is an integer month count, clamped;
--   * EXECUTE is revoked from PUBLIC, and dms_app is granted only the
--     bounded rolling-window entry points — never the arbitrary-range
--     ensure_audit_partition_range().

-- ---------------------------------------------------------------------------
-- 1. The safety net: DEFAULT partition.
-- ---------------------------------------------------------------------------
DO $do$
BEGIN
    IF to_regclass('public.audit_events_default') IS NULL THEN
        EXECUTE 'CREATE TABLE public.audit_events_default PARTITION OF public.audit_events DEFAULT';
    END IF;
    -- Mirror the parent's append-only grants. Queries routed through
    -- audit_events check privileges on the parent only, so these matter
    -- solely for direct-partition access by tooling; granting them keeps
    -- runtime-created partitions indistinguishable from migration-created
    -- ones.
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'dms_app') THEN
        EXECUTE 'GRANT SELECT, INSERT ON public.audit_events_default TO dms_app';
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'dms_readonly') THEN
        EXECUTE 'GRANT SELECT ON public.audit_events_default TO dms_readonly';
    END IF;
END
$do$;

-- ---------------------------------------------------------------------------
-- 2a. ensure_audit_partition_range(from, to) — the worker.
--
-- Idempotent: a month whose partition already exists is skipped, so the
-- function is safe to call on every service boot and every tick.
--
-- Why build-then-ATTACH instead of CREATE TABLE ... PARTITION OF: with a
-- DEFAULT partition present, Postgres refuses to create/attach a partition
-- whose range the DEFAULT still holds rows for. Creating the table
-- detached lets us move those rows across first, inside the same
-- transaction, so the relocation is atomic — a crash mid-way leaves every
-- row exactly where it was. The move targets the leaf partition with ONLY,
-- which carries no RLS policies of its own (policies live on the parent),
-- so no tenant context is needed and no cross-tenant data is read: the
-- rows are relayed DELETE ... RETURNING → INSERT without being inspected.
-- ---------------------------------------------------------------------------
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
    -- Never build more than 10 years of partitions in one call, whatever
    -- the caller asked for.
    IF v_last > (v_month + interval '10 years')::date THEN
        v_last := (v_month + interval '10 years')::date;
    END IF;

    v_has_default := to_regclass('public.audit_events_default') IS NOT NULL;

    WHILE v_month <= v_last LOOP
        v_part := 'audit_events_' || to_char(v_month, 'YYYY_MM');
        -- Bounds are absolute instants (the %L literal carries its UTC
        -- offset), so they do not depend on the caller's TimeZone setting.
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

-- ---------------------------------------------------------------------------
-- 2b. ensure_audit_partitions(months_ahead) — the rolling window the
-- service calls. Covers last month (so a redelivered/backdated event has
-- a home, and anything that already fell into DEFAULT last month gets
-- relocated) through months_ahead months forward. Returns how many
-- partitions it created, which the caller logs and counts.
-- ---------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION ensure_audit_partitions(months_ahead integer DEFAULT 12)
RETURNS integer
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $fn$
DECLARE
    v_ahead integer := LEAST(GREATEST(COALESCE(months_ahead, 12), 0), 120);
    v_now   date    := date_trunc('month', now() AT TIME ZONE 'UTC')::date;
BEGIN
    RETURN ensure_audit_partition_range(
        (v_now - interval '1 month')::date,
        (v_now + make_interval(months => v_ahead))::date);
END
$fn$;

-- ---------------------------------------------------------------------------
-- 2c. audit_default_partition_rows() — observability. Anything sitting in
-- DEFAULT is a row the maintenance window failed to anticipate: safe, but
-- it should be zero. The service exports this as a gauge. Returns an
-- aggregate only, so it leaks no tenant data despite being DEFINER.
-- ---------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION audit_default_partition_rows()
RETURNS bigint
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $fn$
DECLARE
    v_n bigint;
BEGIN
    IF to_regclass('public.audit_events_default') IS NULL THEN
        RETURN 0;
    END IF;
    EXECUTE 'SELECT count(*) FROM ONLY public.audit_events_default' INTO v_n;
    RETURN v_n;
END
$fn$;

-- ---------------------------------------------------------------------------
-- 3. Lock down EXECUTE. PUBLIC gets nothing; dms_app gets only the two
-- bounded entry points the service actually calls.
-- ---------------------------------------------------------------------------
REVOKE ALL ON FUNCTION ensure_audit_partition_range(date, date) FROM PUBLIC;
REVOKE ALL ON FUNCTION ensure_audit_partitions(integer)         FROM PUBLIC;
REVOKE ALL ON FUNCTION audit_default_partition_rows()           FROM PUBLIC;

DO $do$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'dms_app') THEN
        EXECUTE 'GRANT EXECUTE ON FUNCTION ensure_audit_partitions(integer) TO dms_app';
        EXECUTE 'GRANT EXECUTE ON FUNCTION audit_default_partition_rows() TO dms_app';
    END IF;
END
$do$;

-- ---------------------------------------------------------------------------
-- 4. Heal now. The explicit range covers the gap this bug opened
-- (2026-08) through 2027-12 regardless of when the migration runs; the
-- rolling call then tops it up for any database migrating later than that.
-- ---------------------------------------------------------------------------
SELECT ensure_audit_partition_range(DATE '2026-08-01', DATE '2027-12-01');
SELECT ensure_audit_partitions(12);

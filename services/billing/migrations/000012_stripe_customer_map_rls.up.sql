-- Review remediation (M-28, DEC-2) — stripe_customer_map becomes a tenant
-- table under RLS, with a sanctioned pre-tenant lookup.
--
-- 000011 left stripe_customer_map without RLS so the Stripe webhook could
-- read it before the tenant is known. That made it a cross-tenant table the
-- app role could read and rewrite at will (any tenant's code path could
-- re-point another tenant's Stripe customer). It carries tenant_id, so it is
-- now ENABLE + FORCE ROW LEVEL SECURITY like every other tenant table, and
-- the map write runs inside the tenant's transaction.
--
-- The webhook's pre-tenant read (Stripe id → tenant) goes through two fixed,
-- exact-match SECURITY DEFINER functions, same shape as
-- services/auth/migrations/000001_pretenant_lookup_functions.up.sql: a
-- dedicated NOLOGIN BYPASSRLS owner holding SELECT on this one table,
-- `SET search_path = pg_catalog, public`, REVOKE ALL FROM PUBLIC, EXECUTE
-- only for dms_app, returning the minimum column (tenant_id). Everything
-- else about the row (e.g. the subscription id) is read in the tenant tx
-- once the tenant is known.
--
-- stripe_processed_events stays non-RLS: it holds only Stripe event ids and
-- types, no tenant data.
--
-- Only billing-owned objects are touched; no FK to another chain's tables.

DO $$
BEGIN
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'dms_billing_lookup') THEN
    CREATE ROLE dms_billing_lookup NOLOGIN BYPASSRLS;
  ELSE
    ALTER ROLE dms_billing_lookup NOLOGIN BYPASSRLS;
  END IF;
END $$;

-- BYPASSRLS skips policies, not grants: the definer owner needs SELECT.
GRANT SELECT ON stripe_customer_map TO dms_billing_lookup;

ALTER TABLE stripe_customer_map ENABLE ROW LEVEL SECURITY;
ALTER TABLE stripe_customer_map FORCE  ROW LEVEL SECURITY;

DROP POLICY IF EXISTS stripe_customer_map_tenant_isolation ON stripe_customer_map;
CREATE POLICY stripe_customer_map_tenant_isolation ON stripe_customer_map
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid)
    WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

-- Tenant for a Stripe customer id (exact match; stripe_customer_id is the PK).
CREATE OR REPLACE FUNCTION billing_lookup_tenant_by_stripe_customer(p_customer text)
RETURNS uuid
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
  SELECT m.tenant_id FROM public.stripe_customer_map m
   WHERE m.stripe_customer_id = p_customer
$$;

-- Tenant for a Stripe subscription id (exact match; partial-unique index).
CREATE OR REPLACE FUNCTION billing_lookup_tenant_by_stripe_subscription(p_subscription text)
RETURNS uuid
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
  SELECT m.tenant_id FROM public.stripe_customer_map m
   WHERE m.stripe_subscription_id = p_subscription
$$;

ALTER FUNCTION billing_lookup_tenant_by_stripe_customer(text)     OWNER TO dms_billing_lookup;
ALTER FUNCTION billing_lookup_tenant_by_stripe_subscription(text) OWNER TO dms_billing_lookup;

REVOKE ALL ON FUNCTION billing_lookup_tenant_by_stripe_customer(text)     FROM PUBLIC;
REVOKE ALL ON FUNCTION billing_lookup_tenant_by_stripe_subscription(text) FROM PUBLIC;

-- dms_app may not exist on a bare database (it is created by init-db /
-- the document chain); grant only when it does, as 000011 does.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'dms_app') THEN
    GRANT EXECUTE ON FUNCTION billing_lookup_tenant_by_stripe_customer(text)     TO dms_app;
    GRANT EXECUTE ON FUNCTION billing_lookup_tenant_by_stripe_subscription(text) TO dms_app;
  END IF;
END $$;

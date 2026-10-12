DROP FUNCTION IF EXISTS billing_lookup_tenant_by_stripe_subscription(text);
DROP FUNCTION IF EXISTS billing_lookup_tenant_by_stripe_customer(text);

DROP POLICY IF EXISTS stripe_customer_map_tenant_isolation ON stripe_customer_map;
ALTER TABLE stripe_customer_map NO FORCE ROW LEVEL SECURITY;
ALTER TABLE stripe_customer_map DISABLE ROW LEVEL SECURITY;

DO $$
BEGIN
  IF EXISTS (SELECT FROM pg_roles WHERE rolname = 'dms_billing_lookup') THEN
    REVOKE ALL ON stripe_customer_map FROM dms_billing_lookup;
    DROP ROLE dms_billing_lookup;
  END IF;
END $$;

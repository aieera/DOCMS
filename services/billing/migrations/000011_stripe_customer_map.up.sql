-- Stripe → tenant mapping + webhook idempotency (money-path fix).
--
-- The Stripe webhook resolves the tenant FROM a Stripe id BEFORE the
-- tenant context exists (the event only carries Stripe ids). The
-- subscriptions table is FORCE ROW LEVEL SECURITY, so the old
-- GetSubscriptionByStripeID (raw pool, no app.current_tenant) fails
-- closed to 0 rows under the prod dms_app (NOBYPASSRLS) role — the
-- webhook could never find the tenant even once ids were persisted.
--
-- These two tables are deliberately NON-RLS platform metadata (the
-- organizations-registry pattern): the webhook reads them pre-tenant to
-- learn the tenant, then does the tenant-scoped subscription write
-- inside WithTenantTx.

-- stripe_customer_map: the authoritative Stripe→tenant lookup, written
-- at checkout.session.completed and at provision time.
CREATE TABLE IF NOT EXISTS stripe_customer_map (
    stripe_customer_id     TEXT PRIMARY KEY,
    stripe_subscription_id TEXT,
    tenant_id              UUID NOT NULL,
    created_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- Resolve a tenant from a subscription id (subscription.* / invoice.*
-- events carry the subscription, not the customer). Partial-unique so a
-- customer's single active subscription id maps to exactly one tenant.
CREATE UNIQUE INDEX IF NOT EXISTS idx_stripe_customer_map_sub
    ON stripe_customer_map (stripe_subscription_id)
    WHERE stripe_subscription_id IS NOT NULL;

-- stripe_processed_events: webhook idempotency. A Stripe event id is
-- recorded here once it has been fully processed; a redelivery of the
-- same id is a no-op ack. Non-RLS (platform-level).
CREATE TABLE IF NOT EXISTS stripe_processed_events (
    event_id     TEXT PRIMARY KEY,
    event_type   TEXT NOT NULL,
    processed_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'dms_app') THEN
        GRANT SELECT, INSERT, UPDATE, DELETE ON stripe_customer_map     TO dms_app;
        GRANT SELECT, INSERT, UPDATE, DELETE ON stripe_processed_events TO dms_app;
    END IF;
END $$;

-- Reconcile billing schema with what services/billing/internal/repository
-- expects. The original DDL (document service 000001_initial_schema) named
-- the tables subscriptions_billing / usage_meters with a long-format usage
-- shape, but the Go code queries `subscriptions` / `usage_records` with
-- wide-format columns. Both tables are empty today; this migration renames
-- and reshapes in place.

BEGIN;

-- ---- subscriptions --------------------------------------------------------
ALTER TABLE subscriptions_billing RENAME TO subscriptions;
ALTER TABLE subscriptions RENAME COLUMN plan TO plan_id;
ALTER TABLE subscriptions ADD COLUMN IF NOT EXISTS grace_period_ends TIMESTAMPTZ;

ALTER INDEX subscriptions_billing_pkey           RENAME TO subscriptions_pkey;
ALTER INDEX idx_sub_billing_stripe_customer      RENAME TO idx_subscriptions_stripe_customer;
ALTER TABLE subscriptions RENAME CONSTRAINT subscriptions_billing_status_check
    TO subscriptions_status_check;
ALTER TABLE subscriptions RENAME CONSTRAINT subscriptions_billing_tenant_id_fkey
    TO subscriptions_tenant_id_fkey;
ALTER POLICY sub_billing_tenant_isolation        ON subscriptions RENAME TO subscriptions_tenant_isolation;
ALTER POLICY sub_billing_tenant_isolation_insert ON subscriptions RENAME TO subscriptions_tenant_isolation_insert;
ALTER TRIGGER update_subscriptions_billing_updated_at ON subscriptions
    RENAME TO update_subscriptions_updated_at;

-- ---- usage_records (reshape from long-format usage_meters) ----------------
ALTER TABLE usage_meters RENAME TO usage_records;

ALTER TABLE usage_records DROP CONSTRAINT usage_meters_pkey;
ALTER TABLE usage_records DROP CONSTRAINT usage_meters_metric_check;
DROP INDEX idx_usage_meters_period;

ALTER TABLE usage_records DROP COLUMN id;
ALTER TABLE usage_records DROP COLUMN metric;
ALTER TABLE usage_records DROP COLUMN quantity;
ALTER TABLE usage_records DROP COLUMN recorded_at;

ALTER TABLE usage_records ADD COLUMN storage_gb   NUMERIC      NOT NULL DEFAULT 0;
ALTER TABLE usage_records ADD COLUMN ocr_pages    BIGINT       NOT NULL DEFAULT 0;
ALTER TABLE usage_records ADD COLUMN api_calls    BIGINT       NOT NULL DEFAULT 0;
ALTER TABLE usage_records ADD COLUMN active_users INT          NOT NULL DEFAULT 0;
ALTER TABLE usage_records ADD COLUMN ai_tokens    BIGINT       NOT NULL DEFAULT 0;

ALTER TABLE usage_records ADD CONSTRAINT usage_records_pkey PRIMARY KEY (tenant_id, period_start);
ALTER TABLE usage_records RENAME CONSTRAINT usage_meters_tenant_id_fkey TO usage_records_tenant_id_fkey;
ALTER POLICY usage_meters_tenant_isolation        ON usage_records RENAME TO usage_records_tenant_isolation;
ALTER POLICY usage_meters_tenant_isolation_insert ON usage_records RENAME TO usage_records_tenant_isolation_insert;

COMMIT;

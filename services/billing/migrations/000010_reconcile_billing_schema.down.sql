-- Reverse 000010_reconcile_billing_schema.up.sql. Restores the original
-- table names, columns, constraints, indexes, policies, and triggers
-- exactly as they were after services/document/migrations/000001.
-- Any rows written in wide format are lost on rollback (no lossless
-- reverse pivot is possible).

BEGIN;

-- ---- usage_records → usage_meters ----------------------------------------
ALTER POLICY usage_records_tenant_isolation_insert ON usage_records RENAME TO usage_meters_tenant_isolation_insert;
ALTER POLICY usage_records_tenant_isolation        ON usage_records RENAME TO usage_meters_tenant_isolation;
ALTER TABLE usage_records RENAME CONSTRAINT usage_records_tenant_id_fkey TO usage_meters_tenant_id_fkey;
ALTER TABLE usage_records DROP CONSTRAINT usage_records_pkey;

ALTER TABLE usage_records DROP COLUMN ai_tokens;
ALTER TABLE usage_records DROP COLUMN active_users;
ALTER TABLE usage_records DROP COLUMN api_calls;
ALTER TABLE usage_records DROP COLUMN ocr_pages;
ALTER TABLE usage_records DROP COLUMN storage_gb;

ALTER TABLE usage_records ADD COLUMN id          UUID        NOT NULL DEFAULT gen_random_uuid();
ALTER TABLE usage_records ADD COLUMN metric      TEXT        NOT NULL;
ALTER TABLE usage_records ADD CONSTRAINT usage_meters_metric_check
    CHECK (metric IN ('storage_gb_days', 'ocr_pages', 'api_calls', 'signatures', 'ai_tokens', 'active_users'));
ALTER TABLE usage_records ADD COLUMN quantity    NUMERIC     NOT NULL;
ALTER TABLE usage_records ADD COLUMN recorded_at TIMESTAMPTZ NOT NULL DEFAULT now();

ALTER TABLE usage_records ADD CONSTRAINT usage_meters_pkey PRIMARY KEY (tenant_id, id);
CREATE INDEX idx_usage_meters_period ON usage_records (tenant_id, metric, period_start);

ALTER TABLE usage_records RENAME TO usage_meters;

-- ---- subscriptions → subscriptions_billing -------------------------------
ALTER TRIGGER update_subscriptions_updated_at ON subscriptions
    RENAME TO update_subscriptions_billing_updated_at;
ALTER POLICY subscriptions_tenant_isolation_insert ON subscriptions RENAME TO sub_billing_tenant_isolation_insert;
ALTER POLICY subscriptions_tenant_isolation        ON subscriptions RENAME TO sub_billing_tenant_isolation;
ALTER TABLE subscriptions RENAME CONSTRAINT subscriptions_tenant_id_fkey
    TO subscriptions_billing_tenant_id_fkey;
ALTER TABLE subscriptions RENAME CONSTRAINT subscriptions_status_check
    TO subscriptions_billing_status_check;
ALTER INDEX idx_subscriptions_stripe_customer RENAME TO idx_sub_billing_stripe_customer;
ALTER INDEX subscriptions_pkey                RENAME TO subscriptions_billing_pkey;

ALTER TABLE subscriptions DROP COLUMN grace_period_ends;
ALTER TABLE subscriptions RENAME COLUMN plan_id TO plan;
ALTER TABLE subscriptions RENAME TO subscriptions_billing;

COMMIT;

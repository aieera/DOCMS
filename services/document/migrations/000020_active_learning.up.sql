-- Active learning pipeline (ADR 0060) — per-tenant DistilBERT
-- fine-tuning. Tables hold training data, model versions, and config;
-- model weights live in MinIO (per-tenant path), never the DB.

-- ---------------------------------------------------------------------------
-- training_examples — labelled (text, category) pairs derived from user
-- corrections (ADR 0059). Each example knows which model version was
-- trained on it, so re-trains only see new data.
-- ---------------------------------------------------------------------------
CREATE TABLE training_examples (
    tenant_id           UUID        NOT NULL REFERENCES organizations(id),
    id                  UUID        NOT NULL DEFAULT gen_random_uuid(),
    document_id         UUID        NOT NULL,
    version_id          UUID        NOT NULL,
    text_content        TEXT        NOT NULL,
    label               TEXT        NOT NULL,           -- corrected category
    original_prediction TEXT,                           -- what the model predicted
    correction_id       UUID        NOT NULL,
    split               TEXT        NOT NULL DEFAULT 'train'
                                    CHECK (split IN ('train','validation','test')),
    used_in_version     TEXT,                           -- model_version that consumed it
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),

    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, document_id)   REFERENCES documents                (tenant_id, id),
    FOREIGN KEY (tenant_id, version_id)    REFERENCES document_versions        (tenant_id, id),
    FOREIGN KEY (tenant_id, correction_id) REFERENCES classification_corrections (tenant_id, id),
    UNIQUE (tenant_id, correction_id)  -- one example per correction
);

CREATE INDEX idx_training_examples_label
    ON training_examples (tenant_id, label);
CREATE INDEX idx_training_examples_unused
    ON training_examples (tenant_id, created_at)
    WHERE used_in_version IS NULL;

ALTER TABLE training_examples ENABLE ROW LEVEL SECURITY;
ALTER TABLE training_examples FORCE  ROW LEVEL SECURITY;
CREATE POLICY training_examples_tenant_isolation ON training_examples
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY training_examples_tenant_isolation_insert ON training_examples
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

-- ---------------------------------------------------------------------------
-- model_versions — one row per fine-tuned model. Lifecycle:
--   training -> evaluating -> (candidate | retired | failed) -> production -> retired
-- Per-tenant + per-model_type UNIQUE on version_tag so reruns can't
-- collide.
-- ---------------------------------------------------------------------------
CREATE TABLE model_versions (
    tenant_id               UUID        NOT NULL REFERENCES organizations(id),
    id                      UUID        NOT NULL DEFAULT gen_random_uuid(),
    model_type              TEXT        NOT NULL DEFAULT 'classification',
    version_tag             TEXT        NOT NULL,
    base_model              TEXT        NOT NULL DEFAULT 'distilbert-base-uncased',
    s3_artifact_path        TEXT        NOT NULL,
    training_examples_count INT         NOT NULL DEFAULT 0,
    training_metrics        JSONB       NOT NULL DEFAULT '{}'::jsonb,
    eval_metrics            JSONB       NOT NULL DEFAULT '{}'::jsonb,
    status                  TEXT        NOT NULL DEFAULT 'training'
                                        CHECK (status IN ('training','evaluating','candidate','production','retired','failed')),
    error_message           TEXT,
    promoted_at             TIMESTAMPTZ,
    promoted_by             UUID,
    retired_at              TIMESTAMPTZ,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT now(),

    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, promoted_by) REFERENCES users (tenant_id, id),
    UNIQUE (tenant_id, model_type, version_tag)
);

-- The classify task hot-path: which model is the active production one?
-- A unique partial index makes it impossible to have two production
-- models for the same (tenant, model_type) at the same time.
CREATE UNIQUE INDEX uniq_model_versions_production
    ON model_versions (tenant_id, model_type)
    WHERE status = 'production';

CREATE INDEX idx_model_versions_status
    ON model_versions (tenant_id, model_type, status, created_at DESC);

ALTER TABLE model_versions ENABLE ROW LEVEL SECURITY;
ALTER TABLE model_versions FORCE  ROW LEVEL SECURITY;
CREATE POLICY model_versions_tenant_isolation ON model_versions
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY model_versions_tenant_isolation_insert ON model_versions
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

CREATE TRIGGER update_model_versions_updated_at
    BEFORE UPDATE ON model_versions
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

-- ---------------------------------------------------------------------------
-- active_learning_config — opt-in per tenant (default disabled because
-- training has real CPU/GPU cost).
-- ---------------------------------------------------------------------------
CREATE TABLE active_learning_config (
    tenant_id                 UUID        NOT NULL PRIMARY KEY REFERENCES organizations(id),
    enabled                   BOOLEAN     NOT NULL DEFAULT false,
    min_examples_for_retrain  INT         NOT NULL DEFAULT 50
                                          CHECK (min_examples_for_retrain >= 10),
    retrain_increment         INT         NOT NULL DEFAULT 25
                                          CHECK (retrain_increment >= 1),
    auto_promote_if_better    BOOLEAN     NOT NULL DEFAULT false,
    min_accuracy_improvement  REAL        NOT NULL DEFAULT 0.02
                                          CHECK (min_accuracy_improvement >= 0),
    train_validation_split    REAL        NOT NULL DEFAULT 0.10
                                          CHECK (train_validation_split BETWEEN 0.0 AND 0.5),
    train_test_split          REAL        NOT NULL DEFAULT 0.10
                                          CHECK (train_test_split BETWEEN 0.0 AND 0.5),
    gpu_queue                 TEXT        NOT NULL DEFAULT 'intelligence-gpu',
    created_at                TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at                TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- Splits must leave at least 50% for training.
    CHECK (train_validation_split + train_test_split <= 0.5)
);

ALTER TABLE active_learning_config ENABLE ROW LEVEL SECURITY;
ALTER TABLE active_learning_config FORCE  ROW LEVEL SECURITY;
CREATE POLICY active_learning_config_tenant_isolation ON active_learning_config
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY active_learning_config_tenant_isolation_insert ON active_learning_config
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

CREATE TRIGGER update_active_learning_config_updated_at
    BEFORE UPDATE ON active_learning_config
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

-- NER pipeline (ADR 0078) — extends document_entities for the expanded
-- entity taxonomy (PII / Financial / Legal / Medical) and adds two
-- supporting tables:
--   * entity_corrections: append-only ledger of label fixes — feeds
--     active learning the same way classification_corrections does.
--   * ner_config: per-tenant LLM toggles (which model to call for the
--     novel/complex types SpaCy can't handle).
--
-- document_entities rows shipped today as one-row-per-detected-entity
-- with no provenance. Adding `source` lets us tell SpaCy from regex from
-- LLM at read time, which the Entities tab uses to dim/highlight and
-- which the redaction + DLP pipelines use for confidence policy.

ALTER TABLE document_entities
    ADD COLUMN source TEXT NOT NULL DEFAULT 'spacy'
        CHECK (source IN ('spacy','llm','regex','manual'));

-- Existing rows mostly come from regex (email/phone/SSN/CC) or SpaCy.
-- We can't tell which without re-scanning, but the `is_pii=true` rows
-- were always regex (the regex matchers set is_pii). Patch those.
UPDATE document_entities SET source = 'regex' WHERE is_pii = true;

CREATE INDEX idx_document_entities_source
    ON document_entities (tenant_id, version_id, source);

-- ---- entity_corrections ---------------------------------------------------
-- Drives both the user-facing "fix this label" action AND the active
-- learning training set for tenant-specific NER heads.
CREATE TABLE entity_corrections (
    tenant_id          UUID        NOT NULL REFERENCES organizations(id),
    id                 UUID        NOT NULL DEFAULT gen_random_uuid(),
    document_id        UUID        NOT NULL,
    version_id         UUID        NOT NULL,
    -- The original (model-emitted) entity. Nullable because a user can
    -- *add* an entity the model missed entirely.
    original_entity_id UUID,
    original_type      TEXT,
    -- The corrected version.
    corrected_type     TEXT        NOT NULL,
    entity_value       TEXT        NOT NULL,
    start_offset       INTEGER     NOT NULL,
    end_offset         INTEGER     NOT NULL,
    correction_action  TEXT        NOT NULL
                                   CHECK (correction_action IN ('relabel','add','delete','confirm')),
    note               TEXT,
    corrected_by       UUID        NOT NULL,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),

    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, document_id) REFERENCES documents         (tenant_id, id),
    FOREIGN KEY (tenant_id, version_id)  REFERENCES document_versions (tenant_id, id),
    FOREIGN KEY (tenant_id, corrected_by) REFERENCES users            (tenant_id, id)
);

CREATE INDEX idx_entity_corrections_doc
    ON entity_corrections (tenant_id, document_id, created_at DESC);
CREATE INDEX idx_entity_corrections_unused
    ON entity_corrections (tenant_id, created_at)
    WHERE correction_action <> 'confirm';

ALTER TABLE entity_corrections ENABLE ROW LEVEL SECURITY;
ALTER TABLE entity_corrections FORCE  ROW LEVEL SECURITY;
CREATE POLICY entity_corrections_tenant_isolation ON entity_corrections
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY entity_corrections_tenant_isolation_insert ON entity_corrections
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

-- ---- ner_config -----------------------------------------------------------
-- Per-tenant NER configuration. Defaults are off — LLM is opt-in because
-- it bills by token and may move data to a third-party provider.
CREATE TABLE ner_config (
    tenant_id            UUID        PRIMARY KEY REFERENCES organizations(id),
    llm_enabled          BOOLEAN     NOT NULL DEFAULT FALSE,
    -- litellm-style model id: "claude-haiku-4-5", "openai/gpt-4o-mini",
    -- "ollama/llama3.1:8b", etc.
    llm_model            TEXT        NOT NULL DEFAULT 'claude-haiku-4-5',
    -- Which entity types to send to the LLM. SpaCy/regex-handled types
    -- stay out of the LLM call regardless.
    llm_entity_types     TEXT[]      NOT NULL DEFAULT ARRAY[
                                            'party_name','effective_date',
                                            'jurisdiction','governing_law',
                                            'account_number','tax_id',
                                            'patient_id','icd_code','cpt_code',
                                            'address','national_id'
                                        ]::TEXT[],
    -- Pack up to N short docs into one LLM prompt for the 10 docs/sec target.
    llm_batch_size       INTEGER     NOT NULL DEFAULT 5
                                     CHECK (llm_batch_size BETWEEN 1 AND 20),
    -- Confidence threshold below which an LLM-emitted entity is dropped.
    llm_min_confidence   REAL        NOT NULL DEFAULT 0.6
                                     CHECK (llm_min_confidence BETWEEN 0.0 AND 1.0),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE ner_config ENABLE ROW LEVEL SECURITY;
ALTER TABLE ner_config FORCE  ROW LEVEL SECURITY;
CREATE POLICY ner_config_tenant_isolation ON ner_config
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY ner_config_tenant_isolation_insert ON ner_config
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

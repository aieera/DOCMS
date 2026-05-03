-- Translation pipeline (ADR 0056) — language detection + on-demand
-- LLM-driven translations stored as separate artifacts. The original
-- document is never modified.

-- ---------------------------------------------------------------------------
-- document_languages — auto-populated post-OCR. One row per version.
-- ---------------------------------------------------------------------------
CREATE TABLE document_languages (
    tenant_id           UUID        NOT NULL REFERENCES organizations(id),
    id                  UUID        NOT NULL DEFAULT gen_random_uuid(),
    document_id         UUID        NOT NULL,
    version_id          UUID        NOT NULL,
    detected_language   TEXT        NOT NULL,           -- ISO 639-1 (en, ar, fr, ...)
    confidence          REAL        NOT NULL CHECK (confidence BETWEEN 0.0 AND 1.0),
    secondary_languages JSONB       NOT NULL DEFAULT '[]'::jsonb,
    detected_at         TIMESTAMPTZ NOT NULL DEFAULT now(),

    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, document_id) REFERENCES documents (tenant_id, id),
    FOREIGN KEY (tenant_id, version_id)  REFERENCES document_versions  (tenant_id, id),
    UNIQUE (tenant_id, version_id)   -- one detection per version
);

CREATE INDEX idx_document_languages_lang
    ON document_languages (tenant_id, detected_language);

ALTER TABLE document_languages ENABLE ROW LEVEL SECURITY;
ALTER TABLE document_languages FORCE  ROW LEVEL SECURITY;
CREATE POLICY document_languages_tenant_isolation ON document_languages
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY document_languages_tenant_isolation_insert ON document_languages
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

-- ---------------------------------------------------------------------------
-- document_translations — one row per (version, target_language).
-- The translated_blob_id slot is reserved for a v2 PDF render; v1
-- stores the translated text inline.
-- ---------------------------------------------------------------------------
CREATE TABLE document_translations (
    tenant_id           UUID        NOT NULL REFERENCES organizations(id),
    id                  UUID        NOT NULL DEFAULT gen_random_uuid(),
    document_id         UUID        NOT NULL,
    version_id          UUID        NOT NULL,
    source_language     TEXT        NOT NULL,
    target_language     TEXT        NOT NULL,
    status              TEXT        NOT NULL DEFAULT 'pending'
                                    CHECK (status IN ('pending','processing','completed','failed')),
    translated_text     TEXT,
    translated_blob_id  UUID,                            -- v2 PDF render
    model_used          TEXT,
    tokens_used         INT,
    word_count          INT,
    translation_time_ms INT,
    error_message       TEXT,
    requested_by        UUID        NOT NULL,
    completed_at        TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),

    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, document_id) REFERENCES documents (tenant_id, id),
    FOREIGN KEY (tenant_id, version_id)  REFERENCES document_versions  (tenant_id, id),
    FOREIGN KEY (tenant_id, requested_by) REFERENCES users    (tenant_id, id),
    UNIQUE (tenant_id, version_id, target_language)
);

CREATE INDEX idx_translations_doc
    ON document_translations (tenant_id, document_id, status);
CREATE INDEX idx_translations_version
    ON document_translations (tenant_id, version_id);

ALTER TABLE document_translations ENABLE ROW LEVEL SECURITY;
ALTER TABLE document_translations FORCE  ROW LEVEL SECURITY;
CREATE POLICY document_translations_tenant_isolation ON document_translations
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY document_translations_tenant_isolation_insert ON document_translations
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

CREATE TRIGGER update_document_translations_updated_at
    BEFORE UPDATE ON document_translations
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

-- ---------------------------------------------------------------------------
-- translation_config — per-tenant.
-- ---------------------------------------------------------------------------
CREATE TABLE translation_config (
    tenant_id           UUID        NOT NULL PRIMARY KEY REFERENCES organizations(id),
    enabled             BOOLEAN     NOT NULL DEFAULT true,
    available_languages TEXT[]      NOT NULL DEFAULT
        ARRAY['en','ar','fr','es','de','zh','ja','ko','hi','pt']::text[],
    default_target      TEXT        NOT NULL DEFAULT 'en',
    max_chars_per_doc   INT         NOT NULL DEFAULT 100000
                                    CHECK (max_chars_per_doc BETWEEN 1000 AND 10000000),
    model_override      TEXT,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE translation_config ENABLE ROW LEVEL SECURITY;
ALTER TABLE translation_config FORCE  ROW LEVEL SECURITY;
CREATE POLICY translation_config_tenant_isolation ON translation_config
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY translation_config_tenant_isolation_insert ON translation_config
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

CREATE TRIGGER update_translation_config_updated_at
    BEFORE UPDATE ON translation_config
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

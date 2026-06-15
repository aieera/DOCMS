-- Structured field extraction (Workstream "Activate structured field
-- extraction"). The intelligence service's extract task pulls business fields
-- (invoice_number, dates, totals, doc numbers for PO/SO/DO/quote, …) out of
-- OCR text per document_class and persists them HERE with PER-FIELD
-- confidence — distinct from the existing extraction_results table, which
-- stores a single jsonb blob with one aggregate confidence.
--
-- These tables are document-service-owned (so the Go OCR/processing read
-- endpoints + RLS live with the rest of the document schema) but written by
-- the intelligence worker over the shared Postgres — the same split already
-- used for ocr_results.

-- One row per extracted field, with its own confidence + extraction method.
CREATE TABLE extracted_fields (
    tenant_id      UUID NOT NULL REFERENCES organizations(id),
    id             UUID NOT NULL DEFAULT gen_random_uuid(),
    version_id     UUID NOT NULL,
    document_id    UUID NOT NULL,
    document_class TEXT NOT NULL DEFAULT '',
    field_key      TEXT NOT NULL,
    value          TEXT,
    confidence     REAL NOT NULL DEFAULT 0.0,
    -- which engine produced this field: regex | llm | hybrid
    method         TEXT NOT NULL DEFAULT 'regex',
    extracted_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id),
    -- One value per (version, field): the extract task replaces the whole
    -- set per version, and the unique key lets callers upsert / dedupe.
    UNIQUE (tenant_id, version_id, field_key),
    FOREIGN KEY (tenant_id, version_id) REFERENCES document_versions(tenant_id, id)
);
CREATE INDEX idx_extracted_fields_version ON extracted_fields(tenant_id, version_id);
-- Routing (Workstream 3) looks up a document by an extracted business key
-- (e.g. invoice_number); index the lookup it will run.
CREATE INDEX idx_extracted_fields_lookup ON extracted_fields(tenant_id, field_key, value);
ALTER TABLE extracted_fields ENABLE ROW LEVEL SECURITY;
ALTER TABLE extracted_fields FORCE  ROW LEVEL SECURITY;
CREATE POLICY extracted_fields_tenant_isolation ON extracted_fields
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY extracted_fields_tenant_isolation_insert ON extracted_fields
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

-- Per-tenant extraction profile: which fields to pull for a document_class.
-- The regex/LLM logic + sensible defaults live in code
-- (services/intelligence/app/extraction_profiles.py); a row here overrides the
-- default field set for one (tenant, class). `fields` is a JSON array of
-- field_key strings, e.g. ["invoice_number","date","total","customer_name"].
CREATE TABLE extraction_profiles (
    tenant_id      UUID NOT NULL REFERENCES organizations(id),
    document_class TEXT NOT NULL,
    fields         JSONB NOT NULL DEFAULT '[]'::jsonb,
    enabled        BOOLEAN NOT NULL DEFAULT TRUE,
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, document_class)
);
ALTER TABLE extraction_profiles ENABLE ROW LEVEL SECURITY;
ALTER TABLE extraction_profiles FORCE  ROW LEVEL SECURITY;
CREATE POLICY extraction_profiles_tenant_isolation ON extraction_profiles
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY extraction_profiles_tenant_isolation_insert ON extraction_profiles
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

-- Admit 'extract' into the ADR-0115 processing-stage taxonomy so the extract
-- task can record_stage() and the GET /processing endpoint surfaces it.
ALTER TABLE document_processing_stages
    DROP CONSTRAINT document_processing_stages_stage_check;
ALTER TABLE document_processing_stages
    ADD CONSTRAINT document_processing_stages_stage_check
        CHECK (stage IN
            ('upload','virus_scan','ocr','ocr_quality','classify',
             'ner','embed','lang_detect','route_suggest','extract'));

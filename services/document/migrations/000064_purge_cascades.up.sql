-- FIX-6 (audit C5) — make ON DELETE CASCADE the schema-level truth.
--
-- The previous schema relied on application-layer code in PurgeDocument
-- to chase down every feature table and delete child rows in the
-- correct order. The comment at services/document/internal/repository/
-- document_repo.go:235 claimed "cascading FKs handle ocr_results,
-- document_chunks" but the actual FKs (migrations 000001, 000008,
-- 000012-000024) were all created with the default NO ACTION. Net
-- effect: PurgeDocument worked only for documents that never received
-- OCR/extraction/chunking/etc. — and FK-violated silently on every
-- AI-processed doc.
--
-- This migration flips every "feature row keyed by document_id /
-- version_id" FK to ON DELETE CASCADE so a hard DELETE on a document
-- or version takes its dependent rows with it atomically.
--
-- Intentionally NOT cascaded:
--   - documents.superseded_by → documents.id (preserves supersede
--     chain after the new version is purged; the FK is informational)
--   - documents.current_version_id → document_versions.id (self-loop
--     pointer; rewritten by SetCurrentVersion, not the FK engine)
--   - tasks.linked_document_id (already SET NULL — generic tasks
--     outlive the document they reference)
--   - workflow_tasks.document_id (already SET NULL — workflow tasks
--     are independent records)
--
-- Postgres does not support ALTER CONSTRAINT to change ON DELETE
-- behavior. We DROP + ADD each FK. The two operations are inside
-- one transaction so there is no window where the FK doesn't exist.

BEGIN;

-- ---- FKs to documents(tenant_id, id) -------------------------------------

DO $$
DECLARE
    -- (table_name, constraint_name, fk_columns) triples. Order:
    -- alphabetical by table to keep the diff readable.
    rec record;
BEGIN
    FOR rec IN
        SELECT * FROM (VALUES
            ('annotations',                'annotations_tenant_id_document_id_fkey',                '(tenant_id, document_id)'),
            ('anomaly_findings',           'anomaly_findings_tenant_id_document_id_fkey',           '(tenant_id, document_id)'),
            ('classification_corrections', 'classification_corrections_tenant_id_document_id_fkey', '(tenant_id, document_id)'),
            ('comments',                   'comments_tenant_id_document_id_fkey',                   '(tenant_id, document_id)'),
            ('compliance_findings',        'compliance_findings_tenant_id_document_id_fkey',        '(tenant_id, document_id)'),
            ('compliance_summary',         'compliance_summary_tenant_id_document_id_fkey',         '(tenant_id, document_id)'),
            ('disposition_candidates',     'disposition_candidates_tenant_id_document_id_fkey',     '(tenant_id, document_id)'),
            ('document_chunks',            'document_chunks_tenant_id_document_id_fkey',            '(tenant_id, document_id)'),
            ('document_fingerprints',      'document_fingerprints_tenant_id_document_id_fkey',      '(tenant_id, document_id)'),
            ('document_languages',         'document_languages_tenant_id_document_id_fkey',         '(tenant_id, document_id)'),
            ('document_redactions',        'document_redactions_tenant_id_document_id_fkey',        '(tenant_id, document_id)'),
            ('document_translations',      'document_translations_tenant_id_document_id_fkey',      '(tenant_id, document_id)'),
            ('document_versions',          'versions_tenant_id_document_id_fkey',                   '(tenant_id, document_id)'),
            ('duplicate_candidates',       'duplicate_candidates_tenant_id_document_id_fkey',           '(tenant_id, document_id)'),
            ('duplicate_candidates',       'duplicate_candidates_tenant_id_candidate_document_id_fkey', '(tenant_id, candidate_document_id)'),
            ('entities',                   'entities_tenant_id_document_id_fkey',                   '(tenant_id, document_id)'),
            ('entity_corrections',         'entity_corrections_tenant_id_document_id_fkey',         '(tenant_id, document_id)'),
            ('filing_history',             'filing_history_tenant_id_document_id_fkey',             '(tenant_id, document_id)'),
            ('legal_hold_documents',       'legal_hold_documents_tenant_id_document_id_fkey',       '(tenant_id, document_id)'),
            ('ocr_quality_scores',         'ocr_quality_scores_tenant_id_document_id_fkey',         '(tenant_id, document_id)'),
            ('ocr_quality_summary',        'ocr_quality_summary_tenant_id_document_id_fkey',        '(tenant_id, document_id)'),
            ('qa_conversations',           'qa_conversations_tenant_id_document_id_fkey',           '(tenant_id, document_id)'),
            ('redaction_candidates',       'redaction_candidates_tenant_id_document_id_fkey',       '(tenant_id, document_id)'),
            ('redaction_jobs',             'redaction_jobs_tenant_id_document_id_fkey',             '(tenant_id, document_id)'),
            ('route_suggestions',          'route_suggestions_tenant_id_document_id_fkey',          '(tenant_id, document_id)'),
            ('share_links',                'share_links_tenant_id_document_id_fkey',                '(tenant_id, document_id)'),
            ('signature_requests',         'signature_requests_tenant_id_document_id_fkey',         '(tenant_id, document_id)'),
            ('tag_suggestions',            'tag_suggestions_tenant_id_document_id_fkey',            '(tenant_id, document_id)'),
            ('training_examples',          'training_examples_tenant_id_document_id_fkey',          '(tenant_id, document_id)'),
            ('upload_sessions',            'upload_sessions_tenant_id_document_id_fkey',            '(tenant_id, document_id)'),
            ('workflow_instances',         'workflow_instances_tenant_id_document_id_fkey',         '(tenant_id, document_id)')
        ) AS t(table_name, constraint_name, fk_cols)
    LOOP
        -- Drop only if it exists in the expected form; tolerate
        -- migrations that may have replaced it differently.
        EXECUTE format('ALTER TABLE %I DROP CONSTRAINT IF EXISTS %I', rec.table_name, rec.constraint_name);
        EXECUTE format(
            'ALTER TABLE %I ADD CONSTRAINT %I FOREIGN KEY %s REFERENCES documents(tenant_id, id) ON DELETE CASCADE',
            rec.table_name, rec.constraint_name, rec.fk_cols
        );
    END LOOP;
END $$;

-- ---- FKs to document_versions(tenant_id, id) ------------------------------

DO $$
DECLARE
    rec record;
BEGIN
    FOR rec IN
        SELECT * FROM (VALUES
            ('annotations',                'annotations_tenant_id_version_id_fkey',                '(tenant_id, version_id)'),
            ('classification_corrections', 'classification_corrections_tenant_id_version_id_fkey', '(tenant_id, version_id)'),
            ('comments',                   'comments_tenant_id_version_id_fkey',                   '(tenant_id, version_id)'),
            ('compliance_findings',        'compliance_findings_tenant_id_version_id_fkey',        '(tenant_id, version_id)'),
            ('compliance_summary',         'compliance_summary_tenant_id_version_id_fkey',         '(tenant_id, version_id)'),
            ('document_chunks',            'document_chunks_tenant_id_version_id_fkey',            '(tenant_id, version_id)'),
            ('document_languages',         'document_languages_tenant_id_version_id_fkey',         '(tenant_id, version_id)'),
            ('document_translations',      'document_translations_tenant_id_version_id_fkey',      '(tenant_id, version_id)'),
            ('entities',                   'entities_tenant_id_version_id_fkey',                   '(tenant_id, version_id)'),
            ('entity_corrections',         'entity_corrections_tenant_id_version_id_fkey',         '(tenant_id, version_id)'),
            ('extraction_results',         'extraction_results_tenant_id_version_id_fkey',         '(tenant_id, version_id)'),
            ('ocr_quality_scores',         'ocr_quality_scores_tenant_id_version_id_fkey',         '(tenant_id, version_id)'),
            ('ocr_quality_summary',        'ocr_quality_summary_tenant_id_version_id_fkey',        '(tenant_id, version_id)'),
            ('ocr_results',                'ocr_results_tenant_id_version_id_fkey',                '(tenant_id, version_id)'),
            ('redaction_candidates',       'redaction_candidates_tenant_id_version_id_fkey',       '(tenant_id, version_id)'),
            ('redaction_jobs',             'redaction_jobs_tenant_id_source_version_id_fkey',      '(tenant_id, source_version_id)'),
            ('redaction_jobs',             'redaction_jobs_tenant_id_redacted_version_id_fkey',    '(tenant_id, redacted_version_id)'),
            ('route_suggestions',          'route_suggestions_tenant_id_version_id_fkey',          '(tenant_id, version_id)'),
            ('signature_requests',         'signature_requests_tenant_id_version_id_fkey',         '(tenant_id, version_id)'),
            ('tag_suggestions',            'tag_suggestions_tenant_id_version_id_fkey',            '(tenant_id, version_id)'),
            ('training_examples',          'training_examples_tenant_id_version_id_fkey',          '(tenant_id, version_id)')
        ) AS t(table_name, constraint_name, fk_cols)
    LOOP
        EXECUTE format('ALTER TABLE %I DROP CONSTRAINT IF EXISTS %I', rec.table_name, rec.constraint_name);
        EXECUTE format(
            'ALTER TABLE %I ADD CONSTRAINT %I FOREIGN KEY %s REFERENCES document_versions(tenant_id, id) ON DELETE CASCADE',
            rec.table_name, rec.constraint_name, rec.fk_cols
        );
    END LOOP;
END $$;

COMMIT;

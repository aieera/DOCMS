-- Restore NO ACTION on every FK FIX-6 flipped to CASCADE. Mirror of
-- 000064 up — DROP + ADD without the ON DELETE CASCADE clause.
BEGIN;

DO $$
DECLARE
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
        EXECUTE format('ALTER TABLE %I DROP CONSTRAINT IF EXISTS %I', rec.table_name, rec.constraint_name);
        EXECUTE format(
            'ALTER TABLE %I ADD CONSTRAINT %I FOREIGN KEY %s REFERENCES documents(tenant_id, id)',
            rec.table_name, rec.constraint_name, rec.fk_cols
        );
    END LOOP;
END $$;

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
            'ALTER TABLE %I ADD CONSTRAINT %I FOREIGN KEY %s REFERENCES document_versions(tenant_id, id)',
            rec.table_name, rec.constraint_name, rec.fk_cols
        );
    END LOOP;
END $$;

COMMIT;

-- Restore the original processing-stage taxonomy (drop any 'extract' rows
-- first so the narrower CHECK can be re-applied).
DELETE FROM document_processing_stages WHERE stage = 'extract';
ALTER TABLE document_processing_stages
    DROP CONSTRAINT document_processing_stages_stage_check;
ALTER TABLE document_processing_stages
    ADD CONSTRAINT document_processing_stages_stage_check
        CHECK (stage IN
            ('upload','virus_scan','ocr','ocr_quality','classify',
             'ner','embed','lang_detect','route_suggest'));

DROP TABLE IF EXISTS extraction_profiles;
DROP TABLE IF EXISTS extracted_fields;

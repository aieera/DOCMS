DROP TABLE IF EXISTS ner_config;
DROP TABLE IF EXISTS entity_corrections;

DROP INDEX IF EXISTS idx_document_entities_source;
ALTER TABLE document_entities DROP COLUMN IF EXISTS source;

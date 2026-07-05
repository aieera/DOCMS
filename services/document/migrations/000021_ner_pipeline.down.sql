DROP TABLE IF EXISTS ner_config;
DROP TABLE IF EXISTS entity_corrections;

DROP INDEX IF EXISTS idx_document_entities_source;
ALTER TABLE document_entities DROP COLUMN IF EXISTS source;

-- Deliberately does NOT drop the document_entities base table even
-- though the up-migration may have created it (ADR 0121): on a shared
-- database the intelligence service reads/writes the table, and its
-- own chain (intelligence 000002) considers it present. Rolling back
-- the taxonomy extension must not destroy NER data.

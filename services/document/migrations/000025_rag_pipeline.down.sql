DROP TABLE IF EXISTS workspace_ai_settings;
DROP TABLE IF EXISTS rag_query_log;

DROP INDEX IF EXISTS idx_document_chunks_section;
ALTER TABLE document_chunks
    DROP COLUMN IF EXISTS page_number,
    DROP COLUMN IF EXISTS char_offset_end,
    DROP COLUMN IF EXISTS char_offset_start,
    DROP COLUMN IF EXISTS section_path;

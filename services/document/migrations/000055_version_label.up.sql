-- Named versions. Adds an optional human-friendly label to a version
-- row so users can call out anchor versions ("Q1 final", "Approved
-- for legal review") without overloading change_summary. Nullable;
-- existing rows stay NULL. Indexed for the "list named versions for
-- this document" lookup (partial — labelled rows only).

ALTER TABLE document_versions
    ADD COLUMN label TEXT;

CREATE INDEX IF NOT EXISTS idx_document_versions_labeled
    ON document_versions (tenant_id, document_id)
    WHERE label IS NOT NULL;

-- Notes / wiki as a first-class document type.
--
-- A note is a normal document (so it inherits versioning, ACL, search, and
-- audit) distinguished by doc_type, which the tree, search facets, and the
-- collaborative editor key off. Stored as a queryable column rather than in
-- custom_metadata. Defaults to 'file' so every existing row and every
-- INSERT path that doesn't set it is unaffected.
ALTER TABLE documents
    ADD COLUMN IF NOT EXISTS doc_type TEXT NOT NULL DEFAULT 'file'
        CHECK (doc_type IN ('file', 'note', 'wiki'));

-- Partial index to make "list notes/wikis in this tenant" cheap without
-- scanning the (overwhelmingly 'file') majority.
CREATE INDEX IF NOT EXISTS idx_documents_doc_type
    ON documents (tenant_id, doc_type)
    WHERE doc_type <> 'file';

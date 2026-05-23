-- Phase 5 — retention scope (#8).
--
-- Adds folder_filter to retention_policies, retention_exempt columns
-- to documents, and the indexes the sweep needs once the filter
-- AND-clauses are actually wired (see retention.go follow-up note from
-- the original handler).
--
-- Compliance semantics:
--   * retention_exempt is DISTINCT from legal hold (litigation-driven).
--     This column expresses a business waiver: "keep beyond default
--     policy". Audit events use dms.document.retention_exempt_set.v1
--     so an FRCP audit can tell the two apart.
--   * folder_filter scope matches the folder AND every descendant
--     (ltree subtree match via folders.path) — see the sweep query
--     in retention.go.
--   * Sweep target stays { active, retained } — disposed and archived
--     remain terminal.

ALTER TABLE retention_policies
    ADD COLUMN folder_filter UUID;

-- FK with the (tenant_id, folder_id) composite key the schema uses.
ALTER TABLE retention_policies
    ADD CONSTRAINT retention_policies_folder_filter_fk
    FOREIGN KEY (tenant_id, folder_filter)
    REFERENCES folders(tenant_id, id);

ALTER TABLE documents
    ADD COLUMN retention_exempt           BOOLEAN     NOT NULL DEFAULT false,
    ADD COLUMN retention_exempt_reason    TEXT,
    ADD COLUMN retention_exempt_set_by    UUID,
    ADD COLUMN retention_exempt_set_at    TIMESTAMPTZ;

-- Only meaningful when retention_exempt = true; reduces noise in the
-- index. Used to find currently-exempt docs in the per-policy preview.
CREATE INDEX idx_documents_retention_exempt
    ON documents(tenant_id) WHERE retention_exempt;

-- Sweep AND-clause supports — without these the filters are O(table-
-- scan). All three include lifecycle_state so the sweep's
-- `lifecycle_state IN ('active','retained')` predicate is index-
-- friendly even on tenants with millions of docs.
CREATE INDEX idx_documents_workspace_lifecycle
    ON documents(tenant_id, workspace_id, lifecycle_state)
    WHERE deleted_at IS NULL;

CREATE INDEX idx_documents_folder_lifecycle
    ON documents(tenant_id, folder_id, lifecycle_state)
    WHERE deleted_at IS NULL;

CREATE INDEX idx_documents_class_lifecycle
    ON documents(tenant_id, document_class, lifecycle_state)
    WHERE deleted_at IS NULL AND document_class <> '';

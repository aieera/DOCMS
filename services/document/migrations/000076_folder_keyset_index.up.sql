-- Folder hierarchy at 100k (Workstream 6). Keyset-pagination index for
-- ListByParent: the listing scans
--   WHERE tenant_id=? AND workspace_id=? AND parent_folder_id [=? | IS NULL]
--     AND deleted_at IS NULL
--   ORDER BY name ASC, id ASC
--   ... AND (name, id) > (cursor_name, cursor_id)
-- so a parent with 100k direct children is served one bounded page at a time
-- from this index instead of a full sort. Partial (live rows only) to match the
-- other folder indexes (idx_folders_workspace / idx_folders_parent).
--
-- The O(1) per-listing child/document counts reuse the EXISTING indexes:
--   - child_count → idx_folders_parent (tenant_id, parent_folder_id)
--   - doc_count   → idx_documents_workspace_folder (tenant_id, workspace_id, folder_id)
-- so no new count index is needed.
CREATE INDEX idx_folders_keyset
    ON folders (tenant_id, workspace_id, parent_folder_id, name, id)
    WHERE deleted_at IS NULL;

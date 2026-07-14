-- Follow-up to 000098 (review finding): version_id had no FK while its
-- sibling document_id did. References document_versions — the real
-- table name (there is no "versions" table).
ALTER TABLE clause_matches
    ADD CONSTRAINT clause_matches_version_fk
    FOREIGN KEY (tenant_id, version_id)
    REFERENCES document_versions(tenant_id, id) ON DELETE CASCADE;

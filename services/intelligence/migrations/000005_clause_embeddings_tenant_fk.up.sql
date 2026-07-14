-- Follow-up to 000004 (review finding): tenant_id should reference
-- organizations(id) like every other intelligence tenant table.
ALTER TABLE clause_embeddings
    ADD CONSTRAINT clause_embeddings_tenant_fk
    FOREIGN KEY (tenant_id) REFERENCES organizations(id) ON DELETE CASCADE;

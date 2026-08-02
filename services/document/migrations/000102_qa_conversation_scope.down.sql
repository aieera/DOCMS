-- Reverse 000102. Workspace- and global-scoped conversations cannot exist once
-- document_id is NOT NULL again, so remove them first; their qa_messages rows
-- drop via the existing ON DELETE CASCADE.
DELETE FROM qa_conversations WHERE scope <> 'document' OR document_id IS NULL;

DROP INDEX IF EXISTS idx_qa_conversations_scope;

ALTER TABLE qa_conversations
    DROP CONSTRAINT IF EXISTS qa_conversations_scope_ids_chk;

ALTER TABLE qa_conversations
    DROP CONSTRAINT IF EXISTS qa_conversations_tenant_id_workspace_id_fkey;

ALTER TABLE qa_conversations
    DROP COLUMN IF EXISTS workspace_id,
    DROP COLUMN IF EXISTS scope;

ALTER TABLE qa_conversations
    ALTER COLUMN document_id SET NOT NULL;

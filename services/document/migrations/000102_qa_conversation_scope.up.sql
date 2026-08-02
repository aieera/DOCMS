-- Cross-document Ask chat: generalize qa_conversations (previously per-document
-- only) to also support workspace- and tenant-global-scoped conversations. The
-- existing qa_messages table, retrieval/rerank stack, multi-turn assembly, and
-- SSE streaming are all reused — only the conversation row needs a scope, an
-- optional workspace_id, and a nullable document_id.
-- See docs/superpowers/specs/2026-08-02-ask-chat-conversations.md.

ALTER TABLE qa_conversations
    ALTER COLUMN document_id DROP NOT NULL;

ALTER TABLE qa_conversations
    ADD COLUMN scope        TEXT NOT NULL DEFAULT 'document'
        CHECK (scope IN ('document', 'workspace', 'global')),
    ADD COLUMN workspace_id UUID;

-- Composite, tenant-scoped FK mirrors the documents FK pattern. ON DELETE
-- CASCADE so deleting a workspace purges its workspace-scoped chats (matching
-- the doc-scoped CASCADE added in 000064).
ALTER TABLE qa_conversations
    ADD CONSTRAINT qa_conversations_tenant_id_workspace_id_fkey
        FOREIGN KEY (tenant_id, workspace_id)
        REFERENCES workspaces (tenant_id, id)
        ON DELETE CASCADE;

-- Scope integrity: the id column each scope needs is present. Existing rows
-- default to scope='document' and already have document_id NOT NULL, so they
-- satisfy this check when it is added.
ALTER TABLE qa_conversations
    ADD CONSTRAINT qa_conversations_scope_ids_chk CHECK (
        (scope = 'document'  AND document_id IS NOT NULL) OR
        (scope = 'workspace' AND workspace_id IS NOT NULL) OR
        (scope = 'global'    AND document_id IS NULL AND workspace_id IS NULL)
    );

-- History listing for the Ask sidebar: a user's chats within a scope, newest
-- first (workspace_id participates so per-workspace lists stay selective).
CREATE INDEX idx_qa_conversations_scope
    ON qa_conversations (tenant_id, user_id, scope, workspace_id, updated_at DESC);

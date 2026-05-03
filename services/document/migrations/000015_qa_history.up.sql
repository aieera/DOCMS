-- Document Q&A conversation history (ADR 0055).
--
-- One conversation per (document, user) thread; messages append-only.
-- The intelligence service writes here when a /qa endpoint returns a
-- final answer. Used by the chat panel to render history + multi-turn
-- context.

CREATE TABLE qa_conversations (
    tenant_id   UUID        NOT NULL REFERENCES organizations(id),
    id          UUID        NOT NULL DEFAULT gen_random_uuid(),
    document_id UUID        NOT NULL,
    user_id     UUID        NOT NULL,
    title       TEXT,                                 -- auto-generated from first question
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),

    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, document_id) REFERENCES documents (tenant_id, id),
    FOREIGN KEY (tenant_id, user_id)     REFERENCES users     (tenant_id, id)
);

CREATE INDEX idx_qa_conversations_doc
    ON qa_conversations (tenant_id, document_id, updated_at DESC);
CREATE INDEX idx_qa_conversations_user
    ON qa_conversations (tenant_id, user_id, updated_at DESC);

ALTER TABLE qa_conversations ENABLE ROW LEVEL SECURITY;
ALTER TABLE qa_conversations FORCE  ROW LEVEL SECURITY;
CREATE POLICY qa_conversations_tenant_isolation ON qa_conversations
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY qa_conversations_tenant_isolation_insert ON qa_conversations
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

CREATE TRIGGER update_qa_conversations_updated_at
    BEFORE UPDATE ON qa_conversations
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

-- ---------------------------------------------------------------------------
-- qa_messages — append-only thread of (user, assistant) turns.
-- ON DELETE CASCADE so a deleted conversation drops its messages.
-- ---------------------------------------------------------------------------
CREATE TABLE qa_messages (
    tenant_id       UUID        NOT NULL REFERENCES organizations(id),
    id              UUID        NOT NULL DEFAULT gen_random_uuid(),
    conversation_id UUID        NOT NULL,
    role            TEXT        NOT NULL CHECK (role IN ('user','assistant')),
    content         TEXT        NOT NULL,
    citations       JSONB       NOT NULL DEFAULT '[]'::jsonb,
    model_used      TEXT,
    tokens_used     INT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),

    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, conversation_id)
        REFERENCES qa_conversations (tenant_id, id) ON DELETE CASCADE
);

CREATE INDEX idx_qa_messages_conv
    ON qa_messages (tenant_id, conversation_id, created_at);

ALTER TABLE qa_messages ENABLE ROW LEVEL SECURITY;
ALTER TABLE qa_messages FORCE  ROW LEVEL SECURITY;
CREATE POLICY qa_messages_tenant_isolation ON qa_messages
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY qa_messages_tenant_isolation_insert ON qa_messages
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

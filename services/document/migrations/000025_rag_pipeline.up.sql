-- ADR 0063 — RAG pipeline schema additions.
--
-- Builds on the existing document_chunks table that ADR 0055 (Doc Q&A)
-- introduced. The §6.8 spec asks for richer chunk metadata so the
-- workspace-scoped /rag/query endpoint can render precise citations
-- (page X, section Y, char-range start..end) and so the chunker can
-- group structured documents by clause/section rather than the
-- naïve fixed-window split that ADR 0055 shipped with.

-- ---- document_chunks additions -----------------------------------------

ALTER TABLE document_chunks
    -- Hierarchical path through the document's logical structure
    -- (e.g. "Article 5 / Section 5.2 / Clause 5.2.1"). Filled by
    -- the chunker when it can detect headings; null on plain prose.
    ADD COLUMN section_path TEXT,
    -- Per-page-text character offsets into the joined OCR text. Lets
    -- the citation renderer scroll the doc to the exact paragraph
    -- the chunk came from. Null on chunks where the source coords
    -- couldn't be resolved (rare; typically pre-OCR-quality docs).
    ADD COLUMN char_offset_start INTEGER,
    ADD COLUMN char_offset_end   INTEGER,
    -- Single page number (most common case) for citation-display
    -- convenience. Multi-page chunks still carry the full list in
    -- the existing page_numbers array.
    ADD COLUMN page_number       INTEGER;

-- Citations search by (tenant, document, chunk_index) most often;
-- the existing primary key already covers that. Add a partial index
-- for the section browser ("show me all chunks under Article 5").
CREATE INDEX idx_document_chunks_section
    ON document_chunks (tenant_id, document_id, section_path)
    WHERE section_path IS NOT NULL;


-- ---- rag_query_log -----------------------------------------------------
-- Append-only audit + rate-limit ledger for the new
-- POST /rag/query endpoint. Each row captures one workspace-scoped
-- RAG query, the citations returned, and the user's optional thumbs
-- feedback. A ratelimit job sums the day's row count per tenant + user
-- against the configured rag_queries_per_day quota.

CREATE TABLE rag_query_log (
    tenant_id        UUID        NOT NULL REFERENCES organizations(id),
    id               UUID        NOT NULL DEFAULT gen_random_uuid(),
    user_id          UUID        NOT NULL,
    workspace_id     UUID,        -- nullable for tenant-wide queries
    -- Free-form question text. Capped to 4 KB to keep the row small;
    -- longer queries are truncated with an ellipsis at insert time.
    query            TEXT        NOT NULL,
    -- Result the LLM produced. Same 4 KB cap as the query.
    answer           TEXT,
    -- JSON array of citations [{doc_id, chunk_id, page, snippet, score}].
    -- Snapshotted so an audit can see exactly what context the model
    -- was given even if the underlying chunks change later.
    citations        JSONB       NOT NULL DEFAULT '[]'::jsonb,
    -- Quality signal from the user — null until they thumbs.
    feedback         TEXT        CHECK (feedback IN ('up','down','flag')),
    feedback_note    TEXT,
    -- Provenance: which model + retrieval strategy answered.
    model            TEXT,
    retrieval_mode   TEXT        NOT NULL DEFAULT 'hybrid'
                                 CHECK (retrieval_mode IN ('hybrid','vector','bm25')),
    -- Rough cost / latency for the dashboard.
    input_tokens     INTEGER     NOT NULL DEFAULT 0,
    output_tokens    INTEGER     NOT NULL DEFAULT 0,
    cost_usd         NUMERIC(10,6) NOT NULL DEFAULT 0,
    elapsed_ms       INTEGER     NOT NULL DEFAULT 0,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),

    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, user_id) REFERENCES users (tenant_id, id)
);

-- Quota window: count(*) per (tenant, user, last 24h).
CREATE INDEX idx_rag_query_log_user_window
    ON rag_query_log (tenant_id, user_id, created_at DESC);
-- Workspace dashboard ("recent queries here").
CREATE INDEX idx_rag_query_log_workspace
    ON rag_query_log (tenant_id, workspace_id, created_at DESC)
    WHERE workspace_id IS NOT NULL;

ALTER TABLE rag_query_log ENABLE ROW LEVEL SECURITY;
ALTER TABLE rag_query_log FORCE  ROW LEVEL SECURITY;
CREATE POLICY rag_query_log_tenant_isolation ON rag_query_log
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY rag_query_log_tenant_isolation_insert ON rag_query_log
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);


-- ---- workspace_ai_settings --------------------------------------------
-- Per-workspace toggle + model selector for the §6.8 settings panel.
-- Defaults make the AI surface available everywhere; an admin can
-- disable it on a sensitive workspace to keep RAG queries (and the
-- LLM call they trigger) out of that scope entirely.

CREATE TABLE workspace_ai_settings (
    tenant_id              UUID        NOT NULL REFERENCES organizations(id),
    workspace_id           UUID        NOT NULL,
    rag_enabled            BOOLEAN     NOT NULL DEFAULT TRUE,
    -- Embedding model used for new chunks. Existing chunks retain
    -- whatever embedding_model they were created with — switching
    -- requires a re-embed pass per ADR 0063 §"Switching models".
    embedding_model        TEXT        NOT NULL DEFAULT 'bge-large-en-v1.5',
    -- LLM that answers the question. litellm-style provider/model id.
    answer_model           TEXT        NOT NULL DEFAULT 'anthropic/claude-haiku-4-5',
    -- Per-day rate limit per user; 0 disables the gate.
    rag_queries_per_day    INTEGER     NOT NULL DEFAULT 200
                                       CHECK (rag_queries_per_day >= 0),
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT now(),

    PRIMARY KEY (tenant_id, workspace_id),
    FOREIGN KEY (tenant_id, workspace_id) REFERENCES workspaces (tenant_id, id)
);

ALTER TABLE workspace_ai_settings ENABLE ROW LEVEL SECURITY;
ALTER TABLE workspace_ai_settings FORCE  ROW LEVEL SECURITY;
CREATE POLICY workspace_ai_settings_tenant_isolation ON workspace_ai_settings
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY workspace_ai_settings_tenant_isolation_insert ON workspace_ai_settings
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);
